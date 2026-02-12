package provider

import (
	"sync"
	"time"
)

// UnavailableOfferings tracks instance type + region combinations that recently
// failed due to insufficient capacity. Offerings marked unavailable are returned
// with Available=false in GetInstanceTypes, so the scheduler skips them until
// the TTL expires.
type UnavailableOfferings struct {
	cache sync.Map // key: "instanceType:region" → value: time.Time (expiry)
	ttl   time.Duration
}

func NewUnavailableOfferings(ttl time.Duration) *UnavailableOfferings {
	return &UnavailableOfferings{ttl: ttl}
}

// MarkUnavailable records that the given instance type in the given region
// has no capacity. The entry expires after the configured TTL. No-op if u is nil.
func (u *UnavailableOfferings) MarkUnavailable(instanceType, region string) {
	if u == nil {
		return
	}
	u.cache.Store(instanceType+":"+region, time.Now().Add(u.ttl))
}

// IsUnavailable returns true if the instance type + region was recently marked
// as having no capacity and the TTL has not yet expired. Returns false if u is nil.
func (u *UnavailableOfferings) IsUnavailable(instanceType, region string) bool {
	if u == nil {
		return false
	}
	key := instanceType + ":" + region
	v, ok := u.cache.Load(key)
	if !ok {
		return false
	}
	expiry := v.(time.Time)
	if time.Now().After(expiry) {
		u.cache.Delete(key)
		return false
	}
	return true
}
