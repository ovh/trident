package api

import (
	"sync"
	"time"

	"github.com/netapp/trident/pkg/collection"
	"github.com/netapp/trident/storage"
)

const (
	VolumeStatusCreating              = "creating"
	VolumeStatusAvailable             = "available"
	VolumeStatusDeleting              = "deleting"
	VolumeStatusDeleted               = "deleted"
	VolumeStatusError                 = "error"
	VolumeStatusExtending             = "extending"
	VolumeStatusExtendingError        = "extending_error"
	VolumeStatusShrinking             = "shrinking"
	VolumeStatusShrinkingError        = "shrinking_error"
	VolumeStatusReverting             = "reverting"
	VolumeStatusRevertingError        = "reverting_error"
	VolumesStatusCreatingFromSnapshot = "creating_from_snapshot"

	ProtocolTypeNFS  = "NFS"
	ProtocolTypeCIFS = "CIFS"

	PerformanceLevelPremium = "Premium"

	AccessReadOnly  = "ro"
	AccessReadWrite = "rw"

	ExportRuleStatusApplying      = "applying"
	ExportRuleStatusQueuedToApply = "queued_to_apply"
	ExportRuleStatusActive        = "active"
	ExportRuleStatusDenied        = "denied"
	ExportRuleStatusDenying       = "denying"
	ExportRuleStatusError         = "error"

	SnapshotStatusCreating      = "creating"
	SnapshotStatusAvailable     = "available"
	SnapshotStatusError         = "error"
	SnapshotStatusDeleting      = "deleting"
	SnapshotStatusErrorDeleting = "error_deleting"
	SnapshotStatusDeleted       = "deleted"

	SnapshotTimeout = 30 * time.Second
)

// OVHResources is the toplevel cache
// for the set of things we discover
// about our EFS environment.
type OVHResources struct {
	capacityPoolMap *collection.ImmutableMap[string, *CapacityPool]
	storagePools    *collection.ImmutableMap[string, storage.Pool]
	lastUpdateTime  time.Time
	m               sync.Mutex
}

// ovhResourceUpdater swaps in a full discovery snapshot; caller must hold OVHResources.m.
type ovhResourceUpdater func(updateTime time.Time, capacityPoolMap map[string]*CapacityPool)

func newOVHResources() *OVHResources {
	return &OVHResources{
		capacityPoolMap: collection.NewImmutableMap[string, *CapacityPool](nil),
		storagePools:    collection.NewImmutableMap[string, storage.Pool](nil),
	}
}

func (r *OVHResources) newDiscoveryUpdater() ovhResourceUpdater {
	return func(updateTime time.Time, capacityPoolMap map[string]*CapacityPool) {
		r.capacityPoolMap = collection.NewImmutableMap(capacityPoolMap)
		r.lastUpdateTime = updateTime
	}
}

// LockAndCheckStale locks the resources and returns true if the cache is stale based on maxAge.
// The caller must call the returned unlock function when done. When stale is false, updater is nil.
func (r *OVHResources) LockAndCheckStale(maxAge time.Duration) (stale bool, updater ovhResourceUpdater, unlock func()) {
	r.m.Lock()
	if time.Since(r.lastUpdateTime) <= maxAge {
		return false, nil, func() { r.m.Unlock() }
	}
	return true, r.newDiscoveryUpdater(), func() { r.m.Unlock() }
}

// LockForDiscover locks and returns an updater for a full discovery refresh (ignores cache age).
func (r *OVHResources) LockForDiscover() (updater ovhResourceUpdater, unlock func()) {
	r.m.Lock()
	return r.newDiscoveryUpdater(), func() { r.m.Unlock() }
}

// SetStoragePools sets pools from the backend config; not thread-safe and should only be called during initialization.
func (r *OVHResources) SetStoragePools(storagePools map[string]storage.Pool) {
	r.storagePools = collection.NewImmutableMap(storagePools)
}

// GetStoragePools returns the pools defined in the backend config.
func (r *OVHResources) GetStoragePools() *collection.ImmutableMap[string, storage.Pool] {
	return r.storagePools
}

// GetCapacityPools returns the most recently discovered capacity pools.
func (r *OVHResources) GetCapacityPools() *collection.ImmutableMap[string, *CapacityPool] {
	r.m.Lock()
	defer r.m.Unlock()
	return r.capacityPoolMap
}

// CapacityPool records details of a discovered EFS storage pool.
type CapacityPool struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Region       string `json:"region"`
	ServiceLevel string `json:"performanceLevel"`
	Status       string `json:"status"`
}

type EFSVolume struct {
	CreatedAt       time.Time `json:"createdAt"`
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Protocol        string    `json:"protocol"`
	SizeInGigabytes int64     `json:"size"`
	Status          string    `json:"status"`
}

type Volume struct {
	CreatedAt       time.Time
	ID              string
	ServiceID       string
	Name            string
	Protocol        string
	SizeInGigabytes int64
	Status          string
	CreationToken   string
}

type VolumeCreateRequest struct {
	ServiceID       string `json:"-"`
	Name            string `json:"name"`
	Protocol        string `json:"protocol"`
	SizeInGigabytes int64  `json:"size"`
	MountPointName  string `json:"mountPointName"`
	SnapshotID      string `json:"snapshotID"`
}

type VolumeRenameRequest struct {
	Name string `json:"name"`
}

type VolumeResizeRequest struct {
	SizeInGigabytes int64 `json:"size"`
}

type AccessPath struct {
	Path string `json:"path"`
}

type ExportRuleCreateRequest struct {
	AccessLevel string `json:"accessLevel"`
	AccessTo    string `json:"accessTo"`
}

type ExportRule struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	AccessLevel string `json:"accessLevel"`
	AccessTo    string `json:"accessTo"`
}

type EFSSnapshot struct {
	CreatedAt   time.Time `json:"createdAt"`
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Path        string    `json:"path"`
	Status      string    `json:"status"`
	Type        string    `json:"type"`
}

type Snapshot struct {
	CreatedAt time.Time
	ID        string
	ServiceID string
	Name      string
	Path      string
	Status    string
}

type SnapshotCreateRequest struct {
	Name string `json:"name"`
}

type SnapshotRevertRequest struct {
	SnapshotID string `json:"snapshotID"`
}

type Pool struct {
	CreatedAt        time.Time `json:"createdAt"`
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	PerformanceLevel string    `json:"performanceLevel"`
	Quota            int64     `json:"quota"`
	Region           string    `json:"region"`
	Status           string    `json:"status"`
}
