package api

import (
	"context"
	"time"

	"github.com/netapp/trident/storage"
)

//go:generate mockgen -destination=../../../mocks/mock_storage_drivers/mock_ovh/mock_api.go github.com/netapp/trident/storage_drivers/ovh/api OVHClient

type OVHClient interface {
	Init(context.Context, map[string]storage.Pool) error

	RefreshOVHResources(context.Context) error
	CapacityPoolsForStoragePools(context.Context) []*CapacityPool
	CapacityPoolsForStoragePool(context.Context, storage.Pool, string) []*CapacityPool
	EnsureVolumeInValidCapacityPool(context.Context, *Volume) error

	Volumes(context.Context) ([]*Volume, error)
	Volume(context.Context, *storage.VolumeConfig) (*Volume, error)
	VolumeByID(context.Context, string) (*Volume, error)
	VolumeByMountPointName(context.Context, string) (*Volume, error)
	VolumeExists(context.Context, *storage.VolumeConfig) (bool, *Volume, error)
	VolumeExistsByMountPointName(ctx context.Context, token string) (bool, *Volume, error)
	VolumeExistsByID(ctx context.Context, id string) (bool, *Volume, error)
	CreateVolume(context.Context, *VolumeCreateRequest) (*Volume, error)
	WaitForVolumeStatus(context.Context, *Volume, string, []string, time.Duration) (string, error)
	ResizeVolume(context.Context, *Volume, int64) error
	DeleteVolume(context.Context, *Volume) error

	VolumeAccessPaths(context.Context, *Volume) ([]*AccessPath, error)

	ExportRulesForVolume(context.Context, *Volume) ([]*ExportRule, error)
	ExportRuleByID(context.Context, *Volume, string) (*ExportRule, error)
	ExportRulesExists(context.Context, *Volume, string) (bool, []*ExportRule, error)
	CreateExportRule(context.Context, *Volume, *ExportRuleCreateRequest) (*ExportRule, error)
	WaitForExportRuleStatus(context.Context, *Volume, *ExportRule, string, []string, time.Duration) (string, error)
	DeleteExportRule(context.Context, *Volume, *ExportRule) error

	SnapshotsForVolume(context.Context, *Volume) (*[]*Snapshot, error)
	SnapshotForVolume(context.Context, *Volume, string) (*Snapshot, error)
	SnapshotByID(context.Context, *Volume, string) (*Snapshot, error)
	WaitForSnapshotStatus(context.Context, *Volume, *Snapshot, string, []string, time.Duration) error
	CreateSnapshot(context.Context, *Volume, string) (*Snapshot, error)
	RestoreSnapshot(context.Context, *Volume, *Snapshot) error
	DeleteSnapshot(context.Context, *Volume, *Snapshot) error
}
