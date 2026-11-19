package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"shortlink/internal/dao"
	"shortlink/internal/model"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type fakeRepo struct {
	createErrs  []error
	createCalls int
	created     []*model.TLink

	link     *model.TLink
	getErr   error
	getCalls int
}

func (f *fakeRepo) Create(ctx context.Context, link *model.TLink) error {
	idx := f.createCalls
	f.createCalls++
	if idx < len(f.createErrs) && f.createErrs[idx] != nil {
		return f.createErrs[idx]
	}
	f.created = append(f.created, link)
	return nil
}

func (f *fakeRepo) GetByCode(ctx context.Context, code string) (*model.TLink, error) {
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.link, nil
}

type fakeCache struct {
	values    map[string]string
	getErr    error
	setErr    error
	setCalls  int
	incrVal   int64
	incrErr   error
	incrCalls int
}

func (f *fakeCache) Get(ctx context.Context, key string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	if f.values != nil {
		if v, ok := f.values[key]; ok {
			return v, nil
		}
	}
	return "", redis.Nil
}

func (f *fakeCache) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[key] = value
	return nil
}

func (f *fakeCache) Incr(ctx context.Context, key string) (int64, error) {
	f.incrCalls++
	if f.incrErr != nil {
		return 0, f.incrErr
	}
	f.incrVal++
	return f.incrVal, nil
}

type fakeBloom struct {
	exists    map[string]bool
	existsErr error
	addErr    error
	added     []string
}

func (f *fakeBloom) Add(ctx context.Context, code string) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.added = append(f.added, code)
	return nil
}

func (f *fakeBloom) Exists(ctx context.Context, code string) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.exists[code], nil
}

func newService(repo *fakeRepo, cache *fakeCache, bloom *fakeBloom) *LinkService {
	return NewLinkService(repo, cache, bloom)
}

func TestCreateLinkSuccess(t *testing.T) {
	repo := &fakeRepo{}
	cache := &fakeCache{}
	bloom := &fakeBloom{}

	code, err := newService(repo, cache, bloom).CreateLink(context.Background(), "https://go.dev", nil)
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if code != "1" {
		t.Fatalf("code = %q, want 1", code)
	}
	if repo.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", repo.createCalls)
	}
	if len(bloom.added) != 1 || bloom.added[0] != "1" {
		t.Fatalf("bloom added = %v, want [1]", bloom.added)
	}
}

func TestCreateLinkRetriesOnDuplicate(t *testing.T) {
	repo := &fakeRepo{createErrs: []error{gorm.ErrDuplicatedKey, gorm.ErrDuplicatedKey, nil}}
	cache := &fakeCache{}
	bloom := &fakeBloom{}

	code, err := newService(repo, cache, bloom).CreateLink(context.Background(), "https://go.dev", nil)
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if code != "3" {
		t.Fatalf("code = %q, want 3", code)
	}
	if repo.createCalls != 3 {
		t.Fatalf("create calls = %d, want 3", repo.createCalls)
	}
}

func TestCreateLinkConflictExhausted(t *testing.T) {
	repo := &fakeRepo{createErrs: []error{gorm.ErrDuplicatedKey, gorm.ErrDuplicatedKey, gorm.ErrDuplicatedKey}}
	cache := &fakeCache{}
	bloom := &fakeBloom{}

	_, err := newService(repo, cache, bloom).CreateLink(context.Background(), "https://go.dev", nil)
	if !errors.Is(err, ErrCodeConflict) {
		t.Fatalf("err = %v, want ErrCodeConflict", err)
	}
	if len(bloom.added) != 0 {
		t.Fatalf("bloom should not be updated on failure")
	}
}

func TestCreateLinkBloomFailureDoesNotFailCreate(t *testing.T) {
	repo := &fakeRepo{}
	cache := &fakeCache{}
	bloom := &fakeBloom{addErr: errors.New("redis down")}

	code, err := newService(repo, cache, bloom).CreateLink(context.Background(), "https://go.dev", nil)
	if err != nil {
		t.Fatalf("CreateLink should succeed even if bloom add fails: %v", err)
	}
	if code != "1" {
		t.Fatalf("code = %q, want 1", code)
	}
}

func TestCreateLinkWithExpireAt(t *testing.T) {
	repo := &fakeRepo{}
	cache := &fakeCache{}
	bloom := &fakeBloom{}
	expireAt := time.Now().Add(time.Hour)

	_, err := newService(repo, cache, bloom).CreateLink(context.Background(), "https://go.dev", &expireAt)
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if len(repo.created) != 1 || repo.created[0].ExpireAt == nil {
		t.Fatalf("expire_at not stored")
	}
}

func TestResolveCacheHit(t *testing.T) {
	repo := &fakeRepo{}
	cache := &fakeCache{values: map[string]string{"link:abc": "https://go.dev"}}
	bloom := &fakeBloom{}

	origin, err := newService(repo, cache, bloom).Resolve(context.Background(), "abc")
	if err != nil || origin != "https://go.dev" {
		t.Fatalf("Resolve = %q, %v", origin, err)
	}
	if repo.getCalls != 0 {
		t.Fatalf("db should not be queried on cache hit")
	}
}

func TestResolveBloomRejects(t *testing.T) {
	repo := &fakeRepo{}
	cache := &fakeCache{}
	bloom := &fakeBloom{exists: map[string]bool{"abc": false}}

	_, err := newService(repo, cache, bloom).Resolve(context.Background(), "abc")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if repo.getCalls != 0 {
		t.Fatalf("db should not be queried after bloom reject")
	}
}

func TestResolveCacheMissLoadsDB(t *testing.T) {
	repo := &fakeRepo{link: &model.TLink{Code: "abc", Origin: "https://go.dev", Status: 1}}
	cache := &fakeCache{}
	bloom := &fakeBloom{exists: map[string]bool{"abc": true}}

	origin, err := newService(repo, cache, bloom).Resolve(context.Background(), "abc")
	if err != nil || origin != "https://go.dev" {
		t.Fatalf("Resolve = %q, %v", origin, err)
	}
	if repo.getCalls != 1 {
		t.Fatalf("db calls = %d, want 1", repo.getCalls)
	}
	if cache.setCalls != 1 || cache.values["link:abc"] != "https://go.dev" {
		t.Fatalf("cache was not filled")
	}
}

func TestResolveNotFoundWritesNullCache(t *testing.T) {
	repo := &fakeRepo{getErr: dao.ErrNotFound}
	cache := &fakeCache{}
	bloom := &fakeBloom{exists: map[string]bool{"abc": true}}

	_, err := newService(repo, cache, bloom).Resolve(context.Background(), "abc")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if cache.setCalls != 1 || cache.values["link:abc"] != "" {
		t.Fatalf("null cache was not written")
	}
}

func TestResolveExpired(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	repo := &fakeRepo{link: &model.TLink{Code: "abc", Origin: "https://go.dev", Status: 1, ExpireAt: &past}}
	cache := &fakeCache{}
	bloom := &fakeBloom{exists: map[string]bool{"abc": true}}

	_, err := newService(repo, cache, bloom).Resolve(context.Background(), "abc")
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
	if cache.setCalls != 0 {
		t.Fatalf("expired link should not be cached")
	}
}

func TestResolveDisabledReturnsNotFound(t *testing.T) {
	repo := &fakeRepo{link: &model.TLink{Code: "abc", Origin: "https://go.dev", Status: 0}}
	cache := &fakeCache{}
	bloom := &fakeBloom{exists: map[string]bool{"abc": true}}

	_, err := newService(repo, cache, bloom).Resolve(context.Background(), "abc")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if cache.setCalls != 0 {
		t.Fatalf("disabled link should not be cached")
	}
}

func TestResolveBloomErrorDegradesToDB(t *testing.T) {
	repo := &fakeRepo{link: &model.TLink{Code: "abc", Origin: "https://go.dev", Status: 1}}
	cache := &fakeCache{}
	bloom := &fakeBloom{existsErr: errors.New("redis down")}

	origin, err := newService(repo, cache, bloom).Resolve(context.Background(), "abc")
	if err != nil || origin != "https://go.dev" {
		t.Fatalf("Resolve = %q, %v", origin, err)
	}
	if repo.getCalls != 1 {
		t.Fatalf("db should be queried after bloom failure")
	}
}
