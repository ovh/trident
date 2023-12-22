package api

import (
	"time"

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
	StoragePoolMap  map[string]storage.Pool
	CapacityPoolMap map[string]*CapacityPool
	lastUpdateTime  time.Time
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
