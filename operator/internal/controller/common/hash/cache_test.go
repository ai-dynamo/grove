package hash

import (
	"sync"
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

const (
	testPCSName = "test-pcs"
)

func TestGetOrComputeOnCacheMiss(t *testing.T) {
	cache := NewDefaultPodTemplateSpecHashCache(t.Context())
	pclqTemplateSpec := createTestPodCliqueTemplateSpec("test-pclq")
	// Should compute the hash as it's a cache miss
	hash, err := cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec, "high-priority")
	assert.NoError(t, err)
	assert.NotEmpty(t, hash)
	// assert that there is only one entry in the cache
	cache.mu.RLock()
	assert.Equal(t, 1, len(cache.entries))
	cache.mu.RUnlock()
}

func TestGetOrComputeWhenPCSGenerationChanges(t *testing.T) {
	cache := NewDefaultPodTemplateSpecHashCache(t.Context())
	pclqTemplateSpec := createTestPodCliqueTemplateSpec("test-pclq")

	// First call with generation 1
	hash1, err := cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec, "high-priority")
	assert.NoError(t, err)
	assert.NotEmpty(t, hash1)

	// Second call with generation 2 (simulating spec change in controller)
	hash2, err := cache.GetOrCompute(testPCSName, 2, pclqTemplateSpec, "high-priority")
	assert.NoError(t, err)
	assert.NotEmpty(t, hash2)
	// Hashes should be the same since spec content didn't change
	assert.Equal(t, hash1, hash2, "Same spec content should produce same hash")

	// Verify both entries exist in cache with different keys
	cache.mu.RLock()
	key1 := cache.buildKey(testPCSName, 1, pclqTemplateSpec.Name, "high-priority")
	key2 := cache.buildKey(testPCSName, 2, pclqTemplateSpec.Name, "high-priority")
	_, exists1 := cache.entries[key1]
	_, exists2 := cache.entries[key2]
	cache.mu.RUnlock()

	assert.True(t, exists1, "Cache entry for generation 1 should exist")
	assert.True(t, exists2, "Cache entry for generation 2 should exist")
}

func TestGetOrComputeWhenPriorityClassNameChanges(t *testing.T) {
	cache := NewDefaultPodTemplateSpecHashCache(t.Context())
	pclqTemplateSpec := createTestPodCliqueTemplateSpec("test-pclq")

	// First call with priority class "high-priority"
	hash1, err := cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec, "high-priority")
	assert.NoError(t, err)
	assert.NotEmpty(t, hash1)

	// Second call with priority class "low-priority"
	hash2, err := cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec, "low-priority")
	assert.NoError(t, err)
	assert.NotEmpty(t, hash2)

	// Hashes should be different since priority class name is part of the hash input
	assert.NotEqual(t, hash1, hash2, "Different priority class names should produce different hashes")

	// Assert that there are two entries in the cache for the same generation but different priority class names
	cache.mu.RLock()
	key1 := cache.buildKey(testPCSName, 1, pclqTemplateSpec.Name, "high-priority")
	key2 := cache.buildKey(testPCSName, 1, pclqTemplateSpec.Name, "low-priority")
	_, exists1 := cache.entries[key1]
	_, exists2 := cache.entries[key2]
	cache.mu.RUnlock()

	assert.True(t, exists1, "Cache entry for high-priority should exist")
	assert.True(t, exists2, "Cache entry for low-priority should exist")
}

func TestLRUEviction(t *testing.T) {
	var err error
	cache := NewPodTemplateSpecHashCache(t.Context(), 2, 0, 0) // maxSize=2, no TTL

	pclqTemplateSpec1 := createTestPodCliqueTemplateSpec("clique1")
	pclqTemplateSpec2 := createTestPodCliqueTemplateSpec("clique2")
	pclqTemplateSpec3 := createTestPodCliqueTemplateSpec("clique3")

	// Fill cache to max size
	_, err = cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec1, "p1")
	assert.NoError(t, err)
	_, err = cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec2, "p1")
	assert.NoError(t, err)
	// Access clique1 to make clique2 oldest
	_, err = cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec1, "p1")
	assert.NoError(t, err)
	// Add third entry, should evict clique2
	_, err = cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec3, "p1")
	assert.NoError(t, err)

	cache.mu.RLock()
	_, exists1 := cache.entries[cache.buildKey(testPCSName, 1, pclqTemplateSpec1.Name, "p1")]
	_, exists2 := cache.entries[cache.buildKey(testPCSName, 1, pclqTemplateSpec2.Name, "p1")]
	_, exists3 := cache.entries[cache.buildKey(testPCSName, 1, pclqTemplateSpec3.Name, "p1")]
	cache.mu.RUnlock()

	assert.True(t, exists1)
	assert.False(t, exists2) // Should be evicted
	assert.True(t, exists3)
}

func TestTTLEntryExpiry(t *testing.T) {
	cache := NewPodTemplateSpecHashCache(t.Context(), 10, 1*time.Millisecond, 0) // TTL=1ms
	pclqTemplateSpec := createTestPodCliqueTemplateSpec("test-pclq")
	_, _ = cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec, "p1")
	time.Sleep(2 * time.Millisecond)
	cache.cleanupExpiredEntries()
	cache.mu.RLock()
	_, exists := cache.entries[cache.buildKey(testPCSName, 1, pclqTemplateSpec.Name, "p1")]
	cache.mu.RUnlock()
	assert.False(t, exists)
}

func TestBuildKeyUniqueness(t *testing.T) {
	cache := NewDefaultPodTemplateSpecHashCache(t.Context())
	key1 := cache.buildKey(testPCSName, 0, "cliqueA", "p1")
	key2 := cache.buildKey(testPCSName, 0, "cliqueA", "p2")
	key3 := cache.buildKey(testPCSName, 0, "cliqueB", "p1")
	assert.NotEqual(t, key1, key2)
	assert.NotEqual(t, key1, key3)
	assert.NotEqual(t, key2, key3)
}

func TestEvictOldestEmptyCache(t *testing.T) {
	cache := NewDefaultPodTemplateSpecHashCache(t.Context())
	// Should not panic
	cache.evictOldest()
}

func TestConcurrentGetOrCompute(t *testing.T) {
	cache := NewDefaultPodTemplateSpecHashCache(t.Context())
	pclqTemplateSpec := createTestPodCliqueTemplateSpec("test-pclq")
	var wg sync.WaitGroup
	results := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			hash, err := cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec, "p1")
			assert.NoError(t, err)
			results <- hash
			wg.Done()
		}()
	}
	wg.Wait()
	close(results)
	var first string
	for h := range results {
		if first == "" {
			first = h
		} else {
			assert.Equal(t, first, h)
		}
	}
}

// TestExpiredEntriesAreRecomputed verifies that expired entries are properly recomputed
func TestExpiredEntriesAreRecomputed(t *testing.T) {
	cache := NewPodTemplateSpecHashCache(t.Context(), 10, 20*time.Millisecond, 0) // TTL=50ms
	pclqTemplateSpec := createTestPodCliqueTemplateSpec("test-pclq")

	// First call - cache miss
	hash1, err := cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec, "high-priority")
	assert.NoError(t, err)
	assert.NotEmpty(t, hash1)

	// Wait for entry to expire
	time.Sleep(30 * time.Millisecond)

	// Call again - entry expired, should recompute
	hash2, err := cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec, "high-priority")
	assert.NoError(t, err)
	// Hash should be the same since spec didn't change
	assert.Equal(t, hash1, hash2, "Recomputed hash should be same for same spec")
}

func TestConcurrentMixedKeysAndExpiry(t *testing.T) {
	cache := NewPodTemplateSpecHashCache(t.Context(), 100, 10*time.Millisecond, 0) // Short TTL
	pclqTemplateSpec1 := createTestPodCliqueTemplateSpec("test-pclq1")
	pclqTemplateSpec2 := createTestPodCliqueTemplateSpec("test-pclq2")
	pclqTemplateSpec3 := createTestPodCliqueTemplateSpec("test-pclq3")
	pclqTemplateSpec4 := createTestPodCliqueTemplateSpec("test-pclq4")

	entries := []struct {
		cliqueName string
		spec       *grovecorev1alpha1.PodCliqueTemplateSpec
		priority   string
	}{
		{pclqTemplateSpec1.Name, pclqTemplateSpec1, "p1"},
		{pclqTemplateSpec2.Name, pclqTemplateSpec2, "p2"},
		{pclqTemplateSpec3.Name, pclqTemplateSpec3, "p3"},
	}
	var wg sync.WaitGroup
	results := make(chan struct {
		key  string
		hash string
	}, 100)

	// First round: fill cache with all entries
	for _, e := range entries {
		wg.Add(1)
		go func(pclqName, priority string, spec *grovecorev1alpha1.PodCliqueTemplateSpec) {
			hash, err := cache.GetOrCompute(testPCSName, 1, spec, priority)
			assert.NoError(t, err)
			results <- struct{ key, hash string }{cache.buildKey(testPCSName, 1, pclqName, priority), hash}
			wg.Done()
		}(e.cliqueName, e.priority, e.spec)
	}
	wg.Wait()

	// Sleep to allow TTL expiry for some entries
	time.Sleep(15 * time.Millisecond)

	// Second round: access some old entries (should recompute due to expiry), and add a new entry
	for _, e := range entries {
		wg.Add(1)
		go func(pclqName, priority string, spec *grovecorev1alpha1.PodCliqueTemplateSpec) {
			hash, err := cache.GetOrCompute(testPCSName, 1, spec, priority)
			assert.NoError(t, err)
			results <- struct{ key, hash string }{cache.buildKey(testPCSName, 1, pclqName, priority), hash}
			wg.Done()
		}(e.cliqueName, e.priority, e.spec)
	}
	// Add a new entry
	wg.Add(1)
	go func() {
		hash, err := cache.GetOrCompute(testPCSName, 1, pclqTemplateSpec4, "p4")
		assert.NoError(t, err)
		results <- struct{ key, hash string }{cache.buildKey(testPCSName, 1, pclqTemplateSpec4.Name, "p4"), hash}
		wg.Done()
	}()
	wg.Wait()

	// Third round: rapid mixed access, some repeated, some new
	for i := 0; i < 10; i++ {
		for _, e := range entries {
			wg.Add(1)
			go func(pclqName, priority string, spec *grovecorev1alpha1.PodCliqueTemplateSpec) {
				hash, err := cache.GetOrCompute(testPCSName, 1, spec, priority)
				assert.NoError(t, err)
				results <- struct{ key, hash string }{cache.buildKey(testPCSName, 1, pclqName, priority), hash}
				wg.Done()
			}(e.cliqueName, e.priority, e.spec)
		}
	}
	wg.Wait()
	close(results)

	hashMap := make(map[string][]string)
	for r := range results {
		hashMap[r.key] = append(hashMap[r.key], r.hash)
	}

	// For each key, all hashes should be the same, even after expiry and recomputation
	for key, hashes := range hashMap {
		assert.NotEmpty(t, hashes)
		first := hashes[0]
		for _, h := range hashes[1:] {
			assert.Equal(t, first, h, "all hashes for key %s should be equal", key)
		}
	}

	// Ensure all keys are unique
	unique := make(map[string]struct{})
	for key := range hashMap {
		unique[key] = struct{}{}
	}
	assert.GreaterOrEqual(t, len(unique), 3)
}

func createTestPodCliqueTemplateSpec(name string) *grovecorev1alpha1.PodCliqueTemplateSpec {
	return &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name:   name,
		Labels: map[string]string{"app": "test"},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			RoleName: "test-role",
			Replicas: 1,
			PodSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "test", Image: "test:latest"},
				},
			},
		},
	}
}

