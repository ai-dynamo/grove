package hash

import (
	"context"
	"fmt"
	"sync"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

const (
	// DefaultMaxCacheSize is the default maximum number of entries in the cache.
	DefaultMaxCacheSize = 10000
	// DefaultCleanupInterval is how often we check for old entries to evict.
	DefaultCleanupInterval = 5 * time.Minute
	// DefaultEntryTTL is how long an entry can exist without being accessed.
	DefaultEntryTTL = 30 * time.Minute
)

var (
	singletonCache     *PodTemplateSpecHashCache
	singletonCacheOnce sync.Once
)

// GetPodTemplateSpecHashCache returns the Grove-wide singleton instance of PodTemplateSpecHashCache.
// The cache is initialized only once, using the provided context for cleanup goroutine cancellation.
func GetPodTemplateSpecHashCache(ctx context.Context) *PodTemplateSpecHashCache {
	singletonCacheOnce.Do(func() {
		singletonCache = NewDefaultPodTemplateSpecHashCache(ctx)
	})
	return singletonCache
}

type cacheEntry struct {
	lastAccessed time.Time
	hash         string
}

// PodTemplateSpecHashCache provides thread-safe caching for pod template spec hashes with memory management.
// This cache is designed to optimize the performance of hash computations for PodTemplateSpecs, which can be expensive.
// It uses a combination of LRU eviction and TTL-based expiration to manage memory usage effectively while ensuring that frequently accessed hashes remain available.
// Key Features:
// - Thread-safe access with read-write mutex
// - Configurable maximum cache size to prevent unbounded memory growth
// - TTL-based expiration to automatically remove stale entries
// - LRU eviction to prioritize keeping recently accessed entries in the cache
// - Per-PodCliqueSet invalidation to clear relevant cache entries when a PodCliqueSet is updated
// - Context-aware async cleanup to avoid blocking critical reconciliation paths during cache maintenance
type PodTemplateSpecHashCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	// maxSize is the maximum number of entries in the cache.
	// A value of 0 is treated as unlimited, but it is recommended to set a reasonable limit to prevent unbounded memory growth.
	maxSize int
	// entryTTL is the duration after which an entry is considered stale and eligible for eviction if it has not been accessed.
	// A value of 0 means entries do not expire based on time, but it is recommended to set a reasonable TTL to ensure stale entries are eventually removed.
	entryTTL time.Duration
	// cleanupInterval is how often the cache checks for expired entries to evict.
	// A shorter interval means stale entries are removed more quickly, but it may increase CPU usage due to more frequent cleanup. A longer interval reduces CPU usage but may allow stale entries to persist longer.
	// A value of 0 means no periodic cleanup will occur, but it is recommended to set a reasonable interval to ensure timely eviction of stale entries.
	cleanupInterval time.Duration
}

// NewDefaultPodTemplateSpecHashCache creates a new PodTemplateSpecHashCache with default configuration values.
func NewDefaultPodTemplateSpecHashCache(ctx context.Context) *PodTemplateSpecHashCache {
	return NewPodTemplateSpecHashCache(ctx, DefaultMaxCacheSize, DefaultEntryTTL, DefaultCleanupInterval)
}

// NewPodTemplateSpecHashCache creates a new PodTemplateSpecHashCache with the specified configuration values.
// This allows for customization of cache behavior based on specific use cases or performance requirements and
// provides flexibility for testing with different cache configurations.
func NewPodTemplateSpecHashCache(ctx context.Context, maxSize int, entryTTL, cleanupInterval time.Duration) *PodTemplateSpecHashCache {
	cache := &PodTemplateSpecHashCache{
		entries:         make(map[string]*cacheEntry),
		maxSize:         maxSize,
		entryTTL:        entryTTL,
		cleanupInterval: cleanupInterval,
	}
	if cleanupInterval > 0 {
		go cache.cleanupExpiredCacheEntries(ctx)
	}
	return cache
}

// GetOrCompute provides a concurrent-safe function to retrieve the cached pod template hash for the given
// PodCliqueTemplateSpec and priorityClassName, or computes and caches it if not present or expired.
//
// This method implements an optimized caching strategy that:
// 1. Uses a cache key based on PodCliqueSet name and generation, unqualified pclqName, and priorityClassName.
// 2. Relies on PCS generation increments to detect spec changes (handled by the controller).
// 3. Automatically invalidates entries when the TTL expires (based on last access time).
// 4. Cache key changes when:
//   - The PodCliqueSet generation changes (spec content updated by controller)
//   - The priorityClassName changes (creates new cache entry with different key)
//
// Parameters:
//   - pcsName: The name of the PodCliqueSet
//   - pcsGeneration: The generation of the PodCliqueSet (incremented when spec changes)
//   - pclqTemplateSpec: The PodCliqueTemplateSpec containing the pod template specification
//   - priorityClassName: The priority class name for the pod
func (c *PodTemplateSpecHashCache) GetOrCompute(
	pcsName string,
	pcsGeneration int64,
	pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec,
	priorityClassName string) (string, error) {
	key := c.buildKey(pcsName, pcsGeneration, pclqTemplateSpec.Name, priorityClassName)
	now := time.Now()

	// First, try to read from the cache with a read lock
	c.mu.RLock()
	entry, exists := c.entries[key]
	if exists && !c.isEntryExpired(entry, now) {
		// Cache hit - capture hash and update last accessed time
		hashValue := entry.hash
		c.mu.RUnlock()

		// Update last accessed time with write lock
		c.mu.Lock()
		if e, ok := c.entries[key]; ok {
			e.lastAccessed = now
		}
		c.mu.Unlock()
		return hashValue, nil
	}
	c.mu.RUnlock()

	// Cache miss or expired entry - need to compute
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check if another goroutine has already computed the hash while we were waiting for the write lock
	entry, exists = c.entries[key]
	if exists && !c.isEntryExpired(entry, now) {
		entry.lastAccessed = now
		return entry.hash, nil
	}

	// If entry exists but is expired, delete it
	if exists {
		delete(c.entries, key)
	}

	// Enforce max size limit with LRU eviction
	if c.maxSize > 0 && len(c.entries) >= c.maxSize {
		c.evictOldest()
	}

	// Compute and cache the pod template hash
	hash, err := ComputePCLQPodTemplateHash(pclqTemplateSpec, priorityClassName)
	if err != nil {
		return "", err
	}
	c.entries[key] = &cacheEntry{
		hash:         hash,
		lastAccessed: now,
	}
	return hash, nil
}

// cleanupExpiredCacheEntries periodically checks for expired entries in the cache and removes them.
// This should be run in a separate goroutine to avoid blocking critical reconciliation paths during cache maintenance.
// The cleanup process is context-aware, allowing it to be gracefully stopped when the context is canceled (e.g., when the controller is shutting down).
func (c *PodTemplateSpecHashCache) cleanupExpiredCacheEntries(ctx context.Context) {
	if c.cleanupInterval <= 0 {
		return
	}
	ticker := time.NewTicker(c.cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.cleanupExpiredEntries()
		}
	}
}

func (c *PodTemplateSpecHashCache) cleanupExpiredEntries() {
	if c.entryTTL == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.entries {
		if c.isEntryExpired(entry, now) {
			delete(c.entries, key)
		}
	}
}

// isEntryExpired checks if a cache entry is expired based on the last accessed time and the configured entry TTL.
func (c *PodTemplateSpecHashCache) isEntryExpired(entry *cacheEntry, now time.Time) bool {
	if c.entryTTL == 0 {
		return false
	}
	return now.Sub(entry.lastAccessed) > c.entryTTL
}

// buildKey creates a unique cache key for a pod template hash.
// The key format is: "pcsName/pcsGeneration/pclqName/priorityClassName"
func (c *PodTemplateSpecHashCache) buildKey(pcsName string, pcsGeneration int64, pclqName, priorityClassName string) string {
	return fmt.Sprintf("%s/%d/%s/%s", pcsName, pcsGeneration, pclqName, priorityClassName)
}

func (c *PodTemplateSpecHashCache) evictOldest() {
	var (
		oldestKey  string
		oldestTime time.Time
		first      = true
	)
	for key, entry := range c.entries {
		if first || entry.lastAccessed.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.lastAccessed
			first = false
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}
