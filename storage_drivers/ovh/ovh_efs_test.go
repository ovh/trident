package ovh

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/stretchr/testify/assert"
	gomock "go.uber.org/mock/gomock"

	tridentconfig "github.com/netapp/trident/config"
	. "github.com/netapp/trident/logging"
	mockapi "github.com/netapp/trident/mocks/mock_storage_drivers/mock_ovh"
	"github.com/netapp/trident/pkg/convert"
	"github.com/netapp/trident/storage"
	storagefake "github.com/netapp/trident/storage/fake"
	sa "github.com/netapp/trident/storage_attribute"
	drivers "github.com/netapp/trident/storage_drivers"
	"github.com/netapp/trident/storage_drivers/fake"
	"github.com/netapp/trident/storage_drivers/ovh/api"
	"github.com/netapp/trident/utils/errors"
	"github.com/netapp/trident/utils/models"
)

const (
	defaultVolumeSizeStr = "53687091200"

	BackendUUID   = "21ec8941-c845-4675-b7fe-d19413df324e"
	VolumeID      = "2ac741dd-3438-4643-b4b0-879251f92cf4"
	SnapshotID    = "987b71e8-1e08-448c-b5a4-6f6d5b9a7d8a"
	SnapshotName  = "snapshot-987b71e8-1e08-448c-b5a4-6f6d5b9a7d8a"
	VolumeSizeStr = "53687091200"
	VolumeSizeI64 = int64(53687091200)
)

var (
	ctx                  = context.Background()
	errFailed            = errors.New("failed")
	debugTraceFlags      = map[string]bool{"method": true, "api": true, "discovery": true}
	DefaultVolumeSize, _ = strconv.ParseInt(defaultVolumeSizeStr, 10, 64)
)

func TestMain(m *testing.M) {
	// Disable any standard log output
	InitLogOutput(io.Discard)
	os.Exit(m.Run())
}

func newTestEFSDriver(mockAPI api.OVHClient) *NASStorageDriver {
	prefix := "test-"

	config := drivers.OVHNASStorageDriverConfig{
		CommonStorageDriverConfig: &drivers.CommonStorageDriverConfig{
			StorageDriverName: "ovh-netapp-files",
			StoragePrefix:     &prefix,
			DebugTraceFlags:   debugTraceFlags,
		},
		// ServiceID:           api.ServiceID,
		Location:            api.Location,
		ClientID:            api.ClientID,
		ClientSecret:        api.ClientSecret,
		NFSMountOptions:     "rw,hard,rsize=65536,wsize=65536,nfsvers=3,tcp",
		VolumeCreateTimeout: "300",
	}

	return &NASStorageDriver{
		Config:              config,
		API:                 mockAPI,
		volumeCreateTimeout: 300 * time.Second,
	}
}

func newMockEFSDriver(t *testing.T) (*mockapi.MockOVHClient, *NASStorageDriver) {
	mockCtrl := gomock.NewController(t)
	mockAPI := mockapi.NewMockOVHClient(mockCtrl)

	return mockAPI, newTestEFSDriver(mockAPI)
}

func TestName(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	result := driver.Name()

	assert.Equal(t, tridentconfig.OVHNASStorageDriverName, result, "driver name mismatch")
}

func TestBackendName_SetInConfig(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	driver.Config.BackendName = "myOVHEFSBackend"

	result := driver.BackendName()
	assert.Equal(t, "myOVHEFSBackend", result, "backend name mismatch")
}

func TestBackendName_UseDefault(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	driver.Config.BackendName = ""

	result := driver.BackendName()
	assert.Equal(t, "ovhefs_1-cli", result, "backend name mismatch")
}

func TestPoolName(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	driver.Config.BackendName = "myOVHEFSBackend"

	result := driver.poolName("pool-1-A")
	assert.Equal(t, "myOVHEFSBackend_pool1A", result, "pool name mismatch")
}

func TestValidateVolumeName(t *testing.T) {
	tests := []struct {
		Name  string
		Valid bool
	}{
		// Invalid names
		{"", false},
		{"test-_1234453523553252352352352352352352352352352352352352352355235235", false},
		{"1volume", false},
		{"-volume", false},
		{"_volume", false},
		{"volume%", false},
		{"volume_", false},
		{"volume-", false},
		// Valid names
		{"volume", true},
		{"volume1_a", true},
		{"test-12344535235532523523523523523523523523523523523523523523553", true},
	}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			_, driver := newMockEFSDriver(t)

			err := driver.validateVolumeName(test.Name)

			if test.Valid {
				assert.NoError(t, err, "should be valid")
			} else {
				assert.Error(t, err, "should not be valid")
			}
		})
	}
}

func TestValidateCreationToken(t *testing.T) {
	tests := []struct {
		Token string
		Valid bool
	}{
		// Invalid names
		{"", false},
		{"test-_1234453523553252352352352352352352352352352352352352352355235235", false},
		{"1volume", false},
		{"-volume", false},
		{"_volume", false},
		{"volume%", false},
		{"volume_", false},
		{"volume-", false},
		// Valid names
		{"volume", true},
		{"volume1_a", true},
		{"test-12344535235532523523523523523523523523523523523523523523553", true},
	}
	for _, test := range tests {
		t.Run(test.Token, func(t *testing.T) {
			_, driver := newMockEFSDriver(t)

			err := driver.validateCreationToken(test.Token)

			if test.Valid {
				assert.NoError(t, err, "should be valid")
			} else {
				assert.Error(t, err, "should not be valid")
			}
		})
	}
}

func TestDefaultCreateTimeout(t *testing.T) {
	tests := []struct {
		Context  tridentconfig.DriverContext
		Expected time.Duration
	}{
		{tridentconfig.ContextDocker, tridentconfig.DockerCreateTimeout},
		{tridentconfig.ContextCSI, api.VolumeCreateTimeout},
		{"", api.VolumeCreateTimeout},
	}
	for _, test := range tests {
		t.Run(string(test.Context), func(t *testing.T) {
			_, driver := newMockEFSDriver(t)
			driver.Config.DriverContext = test.Context

			result := driver.defaultCreateTimeout()
			assert.Equal(t, test.Expected, result, "mismatched durations")
		})
	}
}

func TestDefaultTimeout(t *testing.T) {
	tests := []struct {
		Context  tridentconfig.DriverContext
		Expected time.Duration
	}{
		{tridentconfig.ContextDocker, tridentconfig.DockerDefaultTimeout},
		{tridentconfig.ContextCSI, api.DefaultTimeout},
		{"", api.DefaultTimeout},
	}
	for _, test := range tests {
		t.Run(string(test.Context), func(t *testing.T) {
			_, driver := newMockEFSDriver(t)
			driver.Config.DriverContext = test.Context

			result := driver.defaultTimeout()
			assert.Equal(t, test.Expected, result, "mistmatched durations")
		})
	}
}

func TestInitialize(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",
		BackendName:       "myOVHBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "eu-west-rbx",
"clientID": "EU.1234",
"clientSecret": "1234",
"clientLocation": "ovh-eu",
"debugTraceFlags": {"method": true, "api": true, "discovery": true}
}
`
	// Have to have at least one Capacity Pool for EFS backends.
	pool := &api.CapacityPool{
		ID:           "1234-1234-1234-1234",
		Name:         "1234-1234-1234-1234",
		ServiceLevel: "premium",
		Region:       "eu-west-rbx",
		Status:       "available",
	}

	mockAPI.EXPECT().Init(ctx, gomock.Any()).Return(nil).Times(1)
	mockAPI.EXPECT().Volumes(ctx).Return([]*api.Volume{}, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePools(ctx).Return([]*api.CapacityPool{pool}).Times(1)
	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		map[string]string{}, api.BackendUUID)

	assert.NoError(t, result, "initialize failed")
	assert.NotNil(t, driver.Config, "config is nil")
	assert.Equal(t, 1, len(driver.pools), "wrong number of pools")
	assert.Equal(t, api.BackendUUID, driver.telemetry.TridentBackendUUID, "wrong backend UUID")
	assert.Equal(t, driver.volumeCreateTimeout, 300*time.Second, "volume timeout mismatch")
	assert.True(t, driver.Initialized(), "driver is not initialized")
}

func TestInitialize_WithSecrets(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",

		BackendName:     "myOVHBackend",
		DriverContext:   tridentconfig.ContextCSI,
		DebugTraceFlags: debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "eu-west-rbx",
"clientID": "EU.1234",
"clientSecret": "1234",
"clientLocation": "ovh-eu",
"debugTraceFlags": {"method": true, "api": true, "discovery": true}
}
`
	secrets := map[string]string{
		"clientid":     api.ClientID,
		"clientsecret": api.ClientSecret,
	}

	// Have to have at least one Capacity Pool for EFS backends.
	pool := &api.CapacityPool{
		ID:           "1234-1234-1234-1234",
		Name:         "1234-1234-1234-1234",
		ServiceLevel: "premium",
		Region:       "eu-west-rbx",
		Status:       "available",
	}

	mockAPI.EXPECT().Init(ctx, gomock.Any()).Return(nil).Times(1)
	mockAPI.EXPECT().Volumes(ctx).Return([]*api.Volume{}, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePools(ctx).Return([]*api.CapacityPool{pool}).Times(1)

	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		secrets, api.BackendUUID)

	assert.NoError(t, result, "initialize failed")
	assert.NotNil(t, driver.Config, "config is nil")
	assert.True(t, driver.Initialized(), "not initialized")
}

func TestInitialize_WithSecrets_WrongClientID(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",
		BackendName:       "myOVHBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "ovh-eu",
"debugTraceFlags": {"method": true, "api": true, "discovery": true}
}
`
	secrets := map[string]string{
		"client_id":    api.ClientID,
		"clientsecret": api.ClientSecret,
	}

	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		secrets, api.BackendUUID)

	assert.Error(t, result, "initialize did not fail")
	assert.False(t, driver.Initialized(), "initialized")
}

func TestInitialize_WithSecrets_WrongClientSecret(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",
		BackendName:       "myOVHBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "ovh-eu",
"debugTraceFlags": {"method": true, "api": true, "discovery": true}
}
`
	secrets := map[string]string{
		"clientid":      api.ClientID,
		"client_secret": api.ClientSecret,
	}

	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		secrets, api.BackendUUID)

	assert.Error(t, result, "initialize did not fail")
	assert.False(t, driver.Initialized(), "initialized")
}

func TestInitialize_InvalidConfigJSON(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",
		BackendName:       "myOVHBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "ovh-eu",
"debugTraceFlags": {"method": true, "api": true, "discovery": true},
}
`
	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		map[string]string{}, api.BackendUUID)

	assert.NotNil(t, driver.Config.CommonStorageDriverConfig, "driver config not set")
	assert.Error(t, result, "initialize did not fail")
	assert.False(t, driver.Initialized(), "initialized")
}

func TestInitialize_InvalidConfigValue(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",
		BackendName:       "myOVHBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "ovh-eu",
"debugTraceFlags": {"method": true, "api": true, "discovery": true},
"volumeCreateTimeout": "yes"
}
`
	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		map[string]string{}, api.BackendUUID)

	assert.NotNil(t, driver.Config.CommonStorageDriverConfig, "driver config not set")
	assert.Error(t, result, "initialize did not fail")
	assert.False(t, driver.Initialized(), "initialized")
}

func TestInitialize_InvalidAPITimeout(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",
		BackendName:       "myOVHBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "ovh-eu",
"debugTraceFlags": {"method": true, "api": true, "discovery": true},
"apiTimeout": "40s"
}
`
	driver.API = nil

	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		map[string]string{}, api.BackendUUID)

	assert.Error(t, result, "initialize did not fail")
	assert.False(t, driver.Initialized(), "initialized")
}

func TestInitialize_InvalidMaxCacheAge(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",
		BackendName:       "myOVHBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "ovh-eu",
"debugTraceFlags": {"method": true, "api": true, "discovery": true},
"maxCacheAge": "400s"
}
`
	driver.API = nil

	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		map[string]string{}, api.BackendUUID)

	assert.Error(t, result, "initialize did not fail")
	assert.False(t, driver.Initialized(), "initialized")
}

func TestInitialize_InvalidPoolAttribute_InvalidStoragePrefix(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	storagePrefix := "&trident"

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",
		StoragePrefix:     &storagePrefix,
		BackendName:       "myOVHBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "ovh-eu",
"debugTraceFlags": {"method": true, "api": true, "discovery": true},
"storagePool": []
}
`
	secrets := map[string]string{
		"clientid":     api.ClientID,
		"clientsecret": api.ClientSecret,
	}

	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		secrets, api.BackendUUID)

	assert.Error(t, result, "initialize did not fail")
	assert.False(t, driver.Initialized(), "initialized")
}

func TestInitialize_InvalidPoolAttribute_InvalidServiceLevel(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",
		BackendName:       "myOVHBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "ovh-eu",
"serviceLevel": "super-premium",
"debugTraceFlags": {"method": true, "api": true, "discovery": true},
"storagePool": []
}
`
	secrets := map[string]string{
		"clientid":     api.ClientID,
		"clientsecret": api.ClientSecret,
	}

	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		secrets, api.BackendUUID)

	assert.Error(t, result, "initialize did not fail")
	assert.False(t, driver.Initialized(), "initialized")
}

func TestInitialize_InvalidPoolAttribute_InvalidExportRule(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",
		BackendName:       "myOVHBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "ovh-eu",
"debugTraceFlags": {"method": true, "api": true, "discovery": true},
"defaults": {"exportRule": "10.10.10.10/128"}
}
`
	secrets := map[string]string{
		"clientid":     api.ClientID,
		"clientsecret": api.ClientSecret,
	}

	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		secrets, api.BackendUUID)

	assert.Error(t, result, "initialize did not fail")
	assert.False(t, driver.Initialized(), "initialized")
}

func TestInitialize_InvalidPoolAttribute_InvalidDefaultVolumeSize(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-netapp-files",
		BackendName:       "myOVHBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	configJSON := `
{
"version": 1,
"storageDriverName": "ovh-efs",
"location": "ovh-eu",
"debugTraceFlags": {"method": true, "api": true, "discovery": true},
"defaults": {"size": "true"}
}
`
	secrets := map[string]string{
		"clientid":     api.ClientID,
		"clientsecret": api.ClientSecret,
	}

	result := driver.Initialize(ctx, tridentconfig.ContextCSI, configJSON, commonConfig,
		secrets, api.BackendUUID)

	assert.Error(t, result, "initialize did not fail")
	assert.False(t, driver.Initialized(), "initialized")
}

func TestInitialized(t *testing.T) {
	tests := []struct {
		Expected bool
	}{
		{true},
		{false},
	}
	for _, test := range tests {
		t.Run(strconv.FormatBool(test.Expected), func(t *testing.T) {
			_, driver := newMockEFSDriver(t)
			driver.initialized = test.Expected

			result := driver.Initialized()

			assert.Equal(t, test.Expected, result, "mismatched initialized values")
		})
	}
}

func TestTerminate(t *testing.T) {
	_, driver := newMockEFSDriver(t)
	driver.initialized = true

	driver.Terminate(ctx, "")
	assert.False(t, driver.initialized, "initialized not false")
}

func TestPopulateConfigurationDefaults_NoneSet(t *testing.T) {
	config := &drivers.OVHNASStorageDriverConfig{
		CommonStorageDriverConfig: &drivers.CommonStorageDriverConfig{
			DriverContext:   tridentconfig.ContextCSI,
			DebugTraceFlags: debugTraceFlags,
		},
	}

	_, driver := newMockEFSDriver(t)
	driver.Config = *config

	result := driver.populateConfigurationDefaults(ctx, &driver.Config)

	assert.NoError(t, result, "an error occured")
	assert.Equal(t, "trident-", *driver.Config.StoragePrefix, "storage prefix mismatch")
	assert.Equal(t, drivers.DefaultVolumeSize, driver.Config.Size, "size mismatch") // TODO: (fix) limit volume size to 100GB when using API for volume creation
	assert.Equal(t, api.PerformanceLevelPremium, driver.Config.ServiceLevel, "service level mismatch")
	assert.Equal(t, defaultNFSMountOptions, driver.Config.NFSMountOptions, "NFS mount options mismatch")
	assert.Equal(t, defaultSnapshotDir, driver.Config.SnapshotDir, "snapshot dir mismatch")
	assert.Equal(t, defaultLimitVolumeSize, driver.Config.LimitVolumeSize, "limit volume size mismatch")
	assert.Equal(t, defaultExportRule, driver.Config.ExportRule, "export rule mismatch")
	assert.Equal(t, sa.NFS, driver.Config.NASType, "NAS type mismatch")
}

func TestPopulateConfigurationDefaults_AllSet(t *testing.T) {
	prefix := "myPrefix"

	config := &drivers.OVHNASStorageDriverConfig{
		CommonStorageDriverConfig: &drivers.CommonStorageDriverConfig{
			DriverContext:   tridentconfig.ContextCSI,
			DebugTraceFlags: debugTraceFlags,
			StoragePrefix:   &prefix,
			LimitVolumeSize: "123456789000",
		},
		NFSMountOptions:     "nfsvers=4.1",
		VolumeCreateTimeout: "300",
		OVHNASStorageDriverPool: drivers.OVHNASStorageDriverPool{
			OVHStorageDriverConfigDefaults: drivers.OVHStorageDriverConfigDefaults{
				CommonStorageDriverConfigDefaults: drivers.CommonStorageDriverConfigDefaults{
					Size: "1234567890",
				},
				ExportRule: "1.1.1.1/32",
			},
			ServiceLevel: "premium",
		},
	}

	_, driver := newMockEFSDriver(t)
	driver.Config = *config

	result := driver.populateConfigurationDefaults(ctx, &driver.Config)

	assert.NoError(t, result, "error occured")

	assert.Equal(t, "myPrefix", *driver.Config.StoragePrefix, "storage prefix mismatch")
	assert.Equal(t, "1234567890", driver.Config.Size, "size mismatch")
	assert.Equal(t, "premium", driver.Config.ServiceLevel, "service level mismatch")
	assert.Equal(t, "nfsvers=4.1", driver.Config.NFSMountOptions, "NFS mount options mismatch")
	assert.Equal(t, "300", driver.Config.VolumeCreateTimeout, "volume create timeout mismatch")
	assert.Equal(t, "123456789000", driver.Config.LimitVolumeSize, "limit volume size mismatch")
	assert.Equal(t, "1.1.1.1/32", driver.Config.ExportRule, "export rule mismatch")
}

func TestInitializeStoragePools_NoVirtualPools(t *testing.T) {
	supportedTopologies := []map[string]string{
		{"topology.kubernetes.io/region": "europe-west-1", "topology.kubernetes.io/zone": "us-east-1c"},
	}

	config := &drivers.OVHNASStorageDriverConfig{
		CommonStorageDriverConfig: &drivers.CommonStorageDriverConfig{
			BackendName:     "myEFSBackend",
			DriverContext:   tridentconfig.ContextCSI,
			DebugTraceFlags: debugTraceFlags,
			LimitVolumeSize: "123456789000",
		},
		NFSMountOptions: "nfsvers=4.1",
		OVHNASStorageDriverPool: drivers.OVHNASStorageDriverPool{
			OVHStorageDriverConfigDefaults: drivers.OVHStorageDriverConfigDefaults{
				CommonStorageDriverConfigDefaults: drivers.CommonStorageDriverConfigDefaults{
					Size: "123456789000",
				},
				ExportRule: "1.1.1.1/32",
			},
			ServiceLevel:        "Premium",
			Region:              "eu-west-rbx",
			SupportedTopologies: supportedTopologies,
		},
	}

	_, driver := newMockEFSDriver(t)
	driver.Config = *config

	// Pool
	pool := storage.NewStoragePool(nil, "myEFSBackend_pool")
	pool.Attributes()[sa.BackendType] = sa.NewStringOffer(driver.Name())
	pool.Attributes()[sa.Snapshots] = sa.NewBoolOffer(true)
	pool.Attributes()[sa.Clones] = sa.NewBoolOffer(true)
	pool.Attributes()[sa.Encryption] = sa.NewBoolOffer(false)
	pool.Attributes()[sa.Replication] = sa.NewBoolOffer(false)
	pool.Attributes()[sa.Labels] = sa.NewLabelOffer(driver.Config.Labels)
	pool.Attributes()[sa.Region] = sa.NewStringOffer("eu-west-rbx")
	pool.Attributes()[sa.NASType] = sa.NewStringOffer("nfs")

	pool.Attributes()[Region] = sa.NewStringOffer("eu-west-rbx")

	pool.InternalAttributes()[Size] = "123456789000"
	pool.InternalAttributes()[ServiceLevel] = api.PerformanceLevelPremium
	pool.InternalAttributes()[SnapshotDir] = defaultSnapshotDir
	pool.InternalAttributes()[ExportRule] = "1.1.1.1/32"
	pool.InternalAttributes()[CapacityPools] = ""

	pool.SetSupportedTopologies(supportedTopologies)

	expectedPools := map[string]storage.Pool{
		"myEFSBackend_pool": pool,
	}

	result := driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)

	assert.NoError(t, result, "error occured")
	assert.Equal(t, expectedPools, driver.pools, "pools do not match")
}

func TestInitializeStoragePools_VirtualPool(t *testing.T) {
	supportedTopologies := []map[string]string{
		{"topology.kubernetes.io/region": "europe-west-1", "topology.kubernetes.io/zone": "us-east-1c"},
	}

	config := &drivers.OVHNASStorageDriverConfig{
		CommonStorageDriverConfig: &drivers.CommonStorageDriverConfig{
			BackendName:     "myEFSBackend",
			DriverContext:   tridentconfig.ContextCSI,
			DebugTraceFlags: debugTraceFlags,
			LimitVolumeSize: "123456789000",
		},
		NFSMountOptions: "nfsvers=4.1",
		OVHNASStorageDriverPool: drivers.OVHNASStorageDriverPool{
			OVHStorageDriverConfigDefaults: drivers.OVHStorageDriverConfigDefaults{
				CommonStorageDriverConfigDefaults: drivers.CommonStorageDriverConfigDefaults{
					Size: "123456789000",
				},
				ExportRule: "1.1.1.1/32",
			},
			ServiceLevel:        "Premium",
			Region:              "eu-west-rbx",
			SupportedTopologies: supportedTopologies,
		},
		Storage: []drivers.OVHNASStorageDriverPool{
			{
				OVHStorageDriverConfigDefaults: drivers.OVHStorageDriverConfigDefaults{
					CommonStorageDriverConfigDefaults: drivers.CommonStorageDriverConfigDefaults{
						Size: "123456789000",
					},
					ExportRule: "2.2.2.2/32",
				},
				CapacityPools:       []string{"3fc4d37b-7005-4bba-8291-a313c218ccac"},
				ServiceLevel:        "Premium",
				Region:              "eu-west-gra",
				SupportedTopologies: supportedTopologies,
				NASType:             "nfs",
			},
			{
				CapacityPools:       []string{"cbb4ad98-23d2-40a9-b99d-469877da830f"},
				SupportedTopologies: supportedTopologies,
				NASType:             "nfs",
			},
		},
	}

	_, driver := newMockEFSDriver(t)
	driver.Config = *config

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)

	pool0 := storage.NewStoragePool(nil, "myEFSBackend_pool_0")
	pool0.Attributes()[sa.BackendType] = sa.NewStringOffer(driver.Name())
	pool0.Attributes()[sa.Snapshots] = sa.NewBoolOffer(true)
	pool0.Attributes()[sa.Clones] = sa.NewBoolOffer(true)
	pool0.Attributes()[sa.Encryption] = sa.NewBoolOffer(false)
	pool0.Attributes()[sa.Replication] = sa.NewBoolOffer(false)
	pool0.Attributes()[sa.Labels] = sa.NewLabelOffer(driver.Config.Labels)
	pool0.Attributes()[sa.Region] = sa.NewStringOffer("eu-west-gra")
	pool0.Attributes()[sa.NASType] = sa.NewStringOffer("nfs")

	pool0.InternalAttributes()[Size] = "123456789000"
	pool0.InternalAttributes()[ServiceLevel] = api.PerformanceLevelPremium
	pool0.InternalAttributes()[SnapshotDir] = "true"
	pool0.InternalAttributes()[ExportRule] = "2.2.2.2/32"
	pool0.InternalAttributes()[CapacityPools] = "3fc4d37b-7005-4bba-8291-a313c218ccac"

	pool0.SetSupportedTopologies(supportedTopologies)

	pool1 := storage.NewStoragePool(nil, "myEFSBackend_pool_1")
	pool1.Attributes()[sa.BackendType] = sa.NewStringOffer(driver.Name())
	pool1.Attributes()[sa.Snapshots] = sa.NewBoolOffer(true)
	pool1.Attributes()[sa.Clones] = sa.NewBoolOffer(true)
	pool1.Attributes()[sa.Encryption] = sa.NewBoolOffer(false)
	pool1.Attributes()[sa.Replication] = sa.NewBoolOffer(false)
	pool1.Attributes()[sa.Labels] = sa.NewLabelOffer(driver.Config.Labels)
	pool1.Attributes()[sa.Region] = sa.NewStringOffer("eu-west-rbx")
	pool1.Attributes()[sa.NASType] = sa.NewStringOffer("nfs")

	pool1.InternalAttributes()[Size] = "123456789000"
	pool1.InternalAttributes()[ServiceLevel] = api.PerformanceLevelPremium
	pool1.InternalAttributes()[SnapshotDir] = "true"
	pool1.InternalAttributes()[ExportRule] = "1.1.1.1/32"
	pool1.InternalAttributes()[CapacityPools] = "cbb4ad98-23d2-40a9-b99d-469877da830f"

	pool1.SetSupportedTopologies(supportedTopologies)

	expectedPools := map[string]storage.Pool{
		"myEFSBackend_pool_0": pool0,
		"myEFSBackend_pool_1": pool1,
	}

	assert.Equal(t, expectedPools, driver.pools, "pools do not match")
}

func TestValidate_InvalidServiceLevel(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.ServiceLevel = "invalid"

	mockAPI.EXPECT().Volumes(ctx).Return([]*api.Volume{}, nil).Times(1)

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	result := driver.validate(ctx)

	assert.Error(t, result, "validate did not fail")
}

func TestValidate_InvalidExportRules(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.ExportRule = "1.2.3.4.5"

	mockAPI.EXPECT().Volumes(ctx).Return([]*api.Volume{}, nil).Times(1)

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	result := driver.validate(ctx)

	assert.Error(t, result, "validate did not fail")
}

func TestValidate_InvalidSize(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.Size = "abcde"

	mockAPI.EXPECT().Volumes(ctx).Return([]*api.Volume{}, nil).Times(1)

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	result := driver.validate(ctx)

	assert.Error(t, result, "validate did not fail")
}

func getStructsForCreateNFSVolume(ctx context.Context, driver *NASStorageDriver, storagePool storage.Pool) (
	*storage.VolumeConfig, *api.CapacityPool, *api.ExportRule, *api.VolumeCreateRequest, *api.Volume,
) {
	volConfig := &storage.VolumeConfig{
		Version:      "1",
		Name:         "testvol1",
		InternalName: "trident-testvol1",
		Size:         VolumeSizeStr,
	}

	capacityPool := &api.CapacityPool{
		ID:           "8e5fa9dd-0daf-432e-9551-bc1ae3a50085",
		Name:         "",
		Region:       "eu-west-rbx",
		ServiceLevel: api.PerformanceLevelPremium,
		Status:       "available",
	}

	exportRule := &api.ExportRule{
		ID:          "",
		Status:      api.ExportRuleStatusApplying,
		AccessLevel: api.AccessReadWrite,
		AccessTo:    "0.0.0.0/0",
	}

	createRequest := &api.VolumeCreateRequest{
		ServiceID:       capacityPool.ID,
		Name:            volConfig.Name,
		SizeInGigabytes: 50,
		Protocol:        api.ProtocolTypeNFS,
		MountPointName:  volConfig.InternalName,
	}

	volumeID := api.CreateVolumeID(capacityPool.ID, "4e82b607-fa62-4706-ad62-995a5bf815be")
	volume := &api.Volume{
		ID:              volumeID,
		Name:            "testvol1",
		CreationToken:   "trident-testvol1",
		Protocol:        api.ProtocolTypeNFS,
		SizeInGigabytes: 100,
		Status:          api.VolumeStatusAvailable,
	}

	return volConfig, capacityPool, exportRule, createRequest, volume
}

func getMultipleCapacityPoolsForCreateVolume() []*api.CapacityPool {
	return []*api.CapacityPool{
		{
			ID:           "8fb2f3b6-663f-4e52-8a51-8111c68d27ac",
			Name:         "",
			Region:       "eu-west-rbx",
			ServiceLevel: api.PerformanceLevelPremium,
			Status:       "available",
		},
		{
			ID:           "97414fe3-3910-42be-9039-731c6711d032",
			Name:         "",
			Region:       "eu-west-gra",
			ServiceLevel: api.PerformanceLevelPremium,
			Status:       "available",
		},
	}
}

func TestCreate_NFSVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, capacityPool, _, createRequest, volume := getStructsForCreateNFSVolume(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePool(ctx, storagePool,
		api.PerformanceLevelPremium).Return([]*api.CapacityPool{capacityPool}).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, createRequest).Return(volume, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)
	// Default driver ACL is 0.0.0.0/0
	mockAPI.EXPECT().CreateExportRule(ctx, volume, &api.ExportRuleCreateRequest{AccessLevel: api.AccessReadWrite, AccessTo: "0.0.0.0/0"}).Return(nil, nil).Times(1)
	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, nil, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusActive, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.NoError(t, result, "create failed")
	assert.Equal(t, createRequest.Protocol, api.ProtocolTypeNFS)
	assert.Equal(t, volume.ID, volConfig.InternalID, "internal ID not set on volConfig")
	//	assert.Equal(t, strconv.FormatUint(uint64(createRequest.SizeInGigabytes), 10), volConfig.Size, "size mismatch")
	assert.Equal(t, api.PerformanceLevelPremium, volConfig.ServiceLevel, "service level mismatch")
}

func TestCreate_NFSVolume_MultipleCapacityPools_FirstSucceeds(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, createRequest, volume := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	capacityPools := getMultipleCapacityPoolsForCreateVolume()

	createRequest.ServiceID = capacityPools[0].ID

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePool(ctx, storagePool,
		api.PerformanceLevelPremium).Return(capacityPools).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, createRequest).Return(volume, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)
	// Default driver ACL is 0.0.0.0/0
	mockAPI.EXPECT().CreateExportRule(ctx, volume, &api.ExportRuleCreateRequest{AccessLevel: api.AccessReadWrite, AccessTo: "0.0.0.0/0"}).Return(nil, nil).Times(1)
	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, nil, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusActive, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.NoError(t, result, "create failed")
	assert.Equal(t, createRequest.Protocol, api.ProtocolTypeNFS)
	assert.Equal(t, volume.ID, volConfig.InternalID, "internal ID not set on volConfig")
	//	assert.Equal(t, strconv.FormatUint(uint64(createRequest.SizeInGigabytes), 10), volConfig.Size, "size mismatch")
	assert.Equal(t, api.PerformanceLevelPremium, volConfig.ServiceLevel, "service level mismatch")
}

func TestCreate_NFSVolume_MultipleCapacityPools_SecondSucceeds(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, createRequest, volume := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	capacityPools := getMultipleCapacityPoolsForCreateVolume()

	createRequest1 := *createRequest
	createRequest1.ServiceID = capacityPools[0].ID
	createRequest2 := *createRequest
	createRequest2.ServiceID = capacityPools[1].ID

	volume.ServiceID = capacityPools[1].ID

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePool(ctx, storagePool,
		api.PerformanceLevelPremium).Return(capacityPools).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, &createRequest1).Return(nil, errFailed).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, &createRequest2).Return(volume, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)
	// Default driver ACL is 0.0.0.0/0
	mockAPI.EXPECT().CreateExportRule(ctx, volume, &api.ExportRuleCreateRequest{AccessLevel: api.AccessReadWrite, AccessTo: "0.0.0.0/0"}).Return(nil, nil).Times(1)
	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, nil, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusActive, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.NoError(t, result, "create failed")
	assert.Equal(t, createRequest.Protocol, api.ProtocolTypeNFS)
	assert.Equal(t, volume.ID, volConfig.InternalID, "internal ID not set on volConfig")
	//	assert.Equal(t, strconv.FormatUint(uint64(createRequest.SizeInGigabytes), 10), volConfig.Size, "size mismatch")
	assert.Equal(t, api.PerformanceLevelPremium, volConfig.ServiceLevel, "service level mismatch")
}

func TestCreate_NFSVolume_MultipleCapacityPools_NoneSucceeds(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, createRequest, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	capacityPools := getMultipleCapacityPoolsForCreateVolume()

	createRequest1 := *createRequest
	createRequest1.ServiceID = capacityPools[0].ID
	createRequest2 := *createRequest
	createRequest2.ServiceID = capacityPools[1].ID

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePool(ctx, storagePool,
		api.PerformanceLevelPremium).Return(capacityPools).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, &createRequest1).Return(nil, errFailed).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, &createRequest2).Return(nil, errFailed).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_InvalidVolumeName(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.Name = "1testvol"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_InvalidCreationToken(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.InternalName = "1testvol"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_NoStoragePool(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)

	result := driver.Create(ctx, volConfig, nil, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_NonexistentStoragePool(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]
	driver.pools = nil

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)

	result := driver.Create(ctx, volConfig, nil, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_VolumeExistsCheckFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, errFailed).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_VolumeExistsCreating(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, volume := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volume.Status = api.VolumeStatusCreating

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.IsType(t, errors.VolumeCreatingError(""), result, "not VolumeCreatingError")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_VolumeExists(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, exportRule, _, volume := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volume.Status = api.VolumeStatusAvailable
	exportRule.Status = api.ExportRuleStatusActive

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().ExportRulesExists(ctx, volume, "0.0.0.0/0").Return(true, []*api.ExportRule{exportRule}, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.IsType(t, drivers.NewVolumeExistsError(""), result, "notVolumeExistsError")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_InvalidSize(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.Size = "invalid"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_NegativeSize(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.Size = "-1M"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_ZeroSize(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, capacityPool, _, createRequest, volume := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.Size = "0"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePool(ctx, storagePool, api.PerformanceLevelPremium).Return([]*api.CapacityPool{capacityPool}).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, createRequest).Return(volume, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)
	// Default driver ACL is 0.0.0.0/0
	mockAPI.EXPECT().CreateExportRule(ctx, volume, &api.ExportRuleCreateRequest{AccessLevel: api.AccessReadWrite, AccessTo: "0.0.0.0/0"}).Return(nil, nil).Times(1)
	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, nil, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusActive, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.NoError(t, result, "create failed")
	assert.Equal(t, DefaultVolumeSize, createRequest.SizeInGigabytes*1024*1024*1024, "request size mismatch")
	assert.Equal(t, volConfig.Size, defaultVolumeSizeStr, "config size mismatch")
	assert.Equal(t, volume.ID, volConfig.InternalID, "internal ID not set on volConfig")
}

func TestCreate_BelowMinimumSize(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.Size = "100"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_AboveMaximumSize(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.LimitVolumeSize = "100Gi"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.Size = "200Gi"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_NoCapacityPool(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePool(ctx, storagePool,
		api.PerformanceLevelPremium).Return([]*api.CapacityPool{}).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_NFSVolume_InvalidMountPointOptions(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.MountOptions = "nfsvers=5"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
}

func TestCreate_NFSVolume_CreateFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, capacityPool, _, createRequest, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePool(ctx, storagePool,
		api.PerformanceLevelPremium).Return([]*api.CapacityPool{capacityPool}).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, createRequest).Return(nil, errFailed).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create failed")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestCreate_NFSVolume_BelowEFSMinimumSize(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, capacityPool, _, createRequest, volume := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.Size = strconv.FormatUint(MinimumEFSVolumeSizeBytes-1, 10)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePool(ctx, storagePool,
		api.PerformanceLevelPremium).Return([]*api.CapacityPool{capacityPool}).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, createRequest).Return(volume, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)
	// Default driver ACL is 0.0.0.0/0
	mockAPI.EXPECT().CreateExportRule(ctx, volume, &api.ExportRuleCreateRequest{AccessLevel: api.AccessReadWrite, AccessTo: "0.0.0.0/0"}).Return(nil, nil).Times(1)
	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, nil, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusActive, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.NoError(t, result, "create failed")
	assert.Equal(t, MinimumEFSVolumeSizeBytes, uint64(createRequest.SizeInGigabytes*1024*1024*1024), "request size mismatch")
	assert.Equal(t, volConfig.Size, strconv.FormatUint(MinimumEFSVolumeSizeBytes, 10), "config size mismatch")
	assert.Equal(t, volume.ID, volConfig.InternalID, "internal ID not set on volConfig")
}

// TODO: (feat) multi-az support
// NOTE: EFS cannot create a volume in a specific "zone" instead we ensure that the region was selected correctly.
func TestCreate_NFSVolume_TopologyRegionSelectionSucceeds(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"
	driver.Config.SupportedTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "eu-west-gra", "topology.kubernetes.io/zone": "eu-west-gra-a"},
		{"topology.kubernetes.io/region": "GRA11", "topology.kubernetes.io/zone": "nova"},
	}

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, capacityPool, _, createRequest, volume := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.RequisiteTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "GRA11", "topology.kubernetes.io/zone": "nova"},
	}
	volConfig.PreferredTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "GRA11", "topology.kubernetes.io/zone": "nova"},
	}

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePool(ctx, storagePool, api.PerformanceLevelPremium).Return([]*api.CapacityPool{capacityPool}).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, createRequest).Return(volume, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)
	// Default driver ACL is 0.0.0.0/0
	mockAPI.EXPECT().CreateExportRule(ctx, volume, &api.ExportRuleCreateRequest{AccessLevel: api.AccessReadWrite, AccessTo: "0.0.0.0/0"}).Return(nil, nil).Times(1)
	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, nil, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusActive, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.NoError(t, result, "create failed")
	assert.Equal(t, volume.ID, volConfig.InternalID, "internal ID not set on volConfig")
}

func TestCreate_NFSVolume_TopologyRegionSelectionFails(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"
	driver.Config.SupportedTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "eu-west-gra", "topology.kubernetes.io/zone": "eu-west-gra-a"},
	}

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.RequisiteTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "GRA11", "topology.kubernetes.io/zone": "nova"},
	}
	volConfig.PreferredTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "GRA11", "topology.kubernetes.io/zone": "nova"},
	}

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
}

func TestCreate_NFSVolume_TopologyZoneSelectionSucceeds(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"
	driver.Config.SupportedTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "eu-west-gra", "topology.kubernetes.io/zone": "eu-west-gra-a"},
	}

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, capacityPool, _, createRequest, volume := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.RequisiteTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "eu-west-gra", "topology.kubernetes.io/zone": "eu-west-gra-a"},
		{"topology.kubernetes.io/region": "eu-west-gra", "topology.kubernetes.io/zone": "eu-west-gra-b"},
	}
	volConfig.PreferredTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "eu-west-gra", "topology.kubernetes.io/zone": "eu-west-gra-a"},
	}

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CapacityPoolsForStoragePool(ctx, storagePool, api.PerformanceLevelPremium).Return([]*api.CapacityPool{capacityPool}).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, createRequest).Return(volume, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)
	// Default driver ACL is 0.0.0.0/0
	mockAPI.EXPECT().CreateExportRule(ctx, volume, &api.ExportRuleCreateRequest{AccessLevel: api.AccessReadWrite, AccessTo: "0.0.0.0/0"}).Return(nil, nil).Times(1)
	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, nil, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusActive, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.NoError(t, result, "create failed")
}

func TestCreate_NFSVolume_TopologyZoneSelectionFails(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"
	driver.Config.SupportedTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "eu-west-gra", "topology.kubernetes.io/zone": "eu-west-gra-c"},
	}

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	volConfig, _, _, _, _ := getStructsForCreateNFSVolume(ctx, driver, storagePool)
	volConfig.RequisiteTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "eu-west-gra", "topology.kubernetes.io/zone": "eu-west-gra-a"},
		{"topology.kubernetes.io/region": "eu-west-gra", "topology.kubernetes.io/zone": "eu-west-gra-b"},
	}
	volConfig.PreferredTopologies = []map[string]string{
		{"topology.kubernetes.io/region": "eu-west-gra", "topology.kubernetes.io/zone": "eu-west-gra-a"},
	}

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)

	result := driver.Create(ctx, volConfig, storagePool, nil)

	assert.Error(t, result, "create did not fail")
}

func getStructsForCreateClone(ctx context.Context, driver *NASStorageDriver, storagePool storage.Pool) (
	*storage.VolumeConfig, *storage.VolumeConfig, *api.VolumeCreateRequest, *api.Volume, *api.Volume,
	*api.Snapshot, []*api.ExportRule, *api.ExportRuleCreateRequest,
) {
	sourceVolumeID := api.CreateVolumeID("986901e3-058f-435a-95b2-aa9b1196b341", "74d2214d-b42a-4e14-98fc-0b1c71e69a6a")
	cloneVolumeID := api.CreateVolumeID("986901e3-058f-435a-95b2-aa9b1196b341", "51a7861a-43ad-4da2-a47e-02ed46db2bf9")
	snapshotID := api.CreateSnapshotID("986901e3-058f-435a-95b2-aa9b1196b341",
		"74d2214d-b42a-4e14-98fc-0b1c71e69a6a", api.SnapshotID)

	sourceVolConfig := &storage.VolumeConfig{
		Version:      "1",
		Name:         "pvc-b008a9f1-b394-4c67-a63f-c490d30709d4",
		InternalName: "pvc-b008a9f1-b394-4c67-a63f-c490d30709d4",
		Size:         api.VolumeSizeStr,
		InternalID:   sourceVolumeID,
	}

	cloneVolConfig := &storage.VolumeConfig{
		Version:                   "1",
		Name:                      "pvc-5ba1be37-8db9-40ba-bdb5-968a2204c8ef",
		InternalName:              "pvc-5ba1be37-8db9-40ba-bdb5-968a2204c8ef",
		CloneSourceVolume:         "pvc-b008a9f1-b394-4c67-a63f-c490d30709d4",
		CloneSourceVolumeInternal: "pvc-b008a9f1-b394-4c67-a63f-c490d30709d4",
	}

	exportRules := []*api.ExportRule{
		{
			AccessLevel: "rw",
			AccessTo:    "10.0.0.10/32",
		},
	}
	// exportPolicy := "10.0.0.10/32"
	createExportRuleRequest := &api.ExportRuleCreateRequest{
		AccessLevel: "rw",
		AccessTo:    "10.0.0.10/32",
	}

	createRequest := &api.VolumeCreateRequest{
		ServiceID:      "986901e3-058f-435a-95b2-aa9b1196b341",
		Name:           cloneVolConfig.Name,
		MountPointName: cloneVolConfig.InternalName,
		// ExportPolicy:      sourceVolume.ExportPolicy,
		Protocol:        api.ProtocolTypeNFS,
		SizeInGigabytes: api.VolumeSizeI64,
		SnapshotID:      api.SnapshotID,
	}

	sourceVolume := &api.Volume{
		ServiceID:       "986901e3-058f-435a-95b2-aa9b1196b341",
		ID:              sourceVolumeID,
		Name:            "pvc-b008a9f1-b394-4c67-a63f-c490d30709d4",
		Protocol:        api.ProtocolTypeNFS,
		Status:          api.VolumeStatusAvailable,
		SizeInGigabytes: api.VolumeSizeI64,
		CreationToken:   "pvc-b008a9f1-b394-4c67-a63f-c490d30709d4",
	}

	cloneVolume := &api.Volume{
		ServiceID:       "986901e3-058f-435a-95b2-aa9b1196b341",
		ID:              cloneVolumeID,
		Name:            "pvc-5ba1be37-8db9-40ba-bdb5-968a2204c8ef",
		Protocol:        api.ProtocolTypeNFS,
		Status:          api.VolumeStatusAvailable,
		SizeInGigabytes: api.VolumeSizeI64,
		CreationToken:   "pvc-5ba1be37-8db9-40ba-bdb5-968a2204c8ef",
	}

	snapshot := &api.Snapshot{
		ServiceID: "986901e3-058f-435a-95b2-aa9b1196b341",
		ID:        snapshotID,
		Name:      "snap1",
		Status:    api.SnapshotStatusAvailable,
	}

	return sourceVolConfig, cloneVolConfig, createRequest, sourceVolume,
		cloneVolume, snapshot, exportRules, createExportRuleRequest
}

func TestCreateClone_NoSnapshot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, createRequest, sourceVolume, cloneVolume, snapshot, exportRules, createExportRulesRequest := getStructsForCreateClone(ctx,
		driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVolume, nil).Times(1)
	mockAPI.EXPECT().VolumeExistsByMountPointName(ctx, cloneVolume.CreationToken).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CreateSnapshot(ctx, sourceVolume, gomock.Any()).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().WaitForSnapshotStatus(ctx, sourceVolume, snapshot, api.SnapshotStatusAvailable, []string{api.SnapshotStatusError}, api.SnapshotTimeout).Return(nil).Times(1)
	mockAPI.EXPECT().ExportRulesForVolume(ctx, sourceVolume).Return(exportRules, nil)
	mockAPI.EXPECT().CreateVolume(ctx, createRequest).Return(cloneVolume, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, cloneVolume, api.VolumeStatusAvailable, []string{api.VolumeStatusError},
		driver.volumeCreateTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)
	mockAPI.EXPECT().CreateExportRule(ctx, cloneVolume, createExportRulesRequest).Return(nil, nil).Times(1)
	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, cloneVolume, nil, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError},
		driver.volumeCreateTimeout).Return(api.ExportRuleStatusActive, nil).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.NoError(t, result, "create failed")
	assert.Equal(t, cloneVolume.ID, cloneVolConfig.InternalID, "internal ID not set on volConfig")
	assert.Equal(t, cloneVolConfig.CloneSourceSnapshotInternal, snapshot.ID,
		"expected snapshot name to be set in CloneSourceSnapshotInternal")
}

func TestCreateClone_Snapshot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, api.BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, createRequest, sourceVol, cloneVol,
		snapshot, exportRules, createExportRuleRequest := getStructsForCreateClone(ctx, driver, storagePool)
	cloneVolConfig.CloneSourceSnapshotInternal = "snap1"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVol, nil).Times(1)
	mockAPI.EXPECT().VolumeExistsByMountPointName(ctx, cloneVol.CreationToken).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, sourceVol, "snap1").Return(snapshot, nil).Times(1)

	mockAPI.EXPECT().ExportRulesForVolume(ctx, sourceVol).Return(exportRules, nil).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, createRequest).Return(cloneVol, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, cloneVol, api.VolumeStatusAvailable, []string{api.VolumeStatusError},
		driver.volumeCreateTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)
	mockAPI.EXPECT().CreateExportRule(ctx, cloneVol, createExportRuleRequest).Return(exportRules[0], nil).Times(1)
	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, cloneVol, exportRules[0], api.ExportRuleStatusActive,
		[]string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusActive, nil)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.NoError(t, result, "clone failed")
	assert.Equal(t, cloneVol.ID, cloneVolConfig.InternalID, "internal ID not set on volConfig")
	assert.NoError(t, result, "error occured")
}

func TestCreateClone_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, _, _, _, _, _ := getStructsForCreateClone(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
}

func TestCreateClone_InvalidVolumeName(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, _, _, _, _, _ := getStructsForCreateClone(ctx, driver, storagePool)
	cloneVolConfig.Name = "1testvol"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
}

func TestCreateClone_InvalidCreationToken(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, _, _, _, _, _ := getStructsForCreateClone(ctx, driver, storagePool)
	cloneVolConfig.InternalName = "1testvol"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
}

func TestCreateClone_NonexistentSourceVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, _, _, _, _, _ := getStructsForCreateClone(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(nil, errors.NotFoundError("not found")).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
}

func TestCreateClone_VolumeExistsCheckFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, sourceVol, _, _, _, _ := getStructsForCreateClone(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVol, nil).Times(1)
	mockAPI.EXPECT().VolumeExistsByMountPointName(ctx, cloneVolConfig.InternalName).Return(false, nil, errFailed).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
}

func TestCreateClone_VolumeExistsCreating(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, sourceVol, cloneVol, _, _, _ := getStructsForCreateClone(ctx, driver, storagePool)
	cloneVol.Status = api.VolumesStatusCreatingFromSnapshot

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVol, nil).Times(1)
	mockAPI.EXPECT().VolumeExistsByMountPointName(ctx, cloneVolConfig.InternalName).Return(true, cloneVol, nil).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
	assert.IsType(t, errors.VolumeCreatingError(""), result, "not VolumeCreatingError")
}

func TestCreateClone_VolumeExistsResourceExhaustedError(t *testing.T) {
	// ???
}

func TestCreateClone_VolumeExists(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, sourceVol, cloneVol, _, _, _ := getStructsForCreateClone(ctx, driver, storagePool)
	cloneVol.Status = api.VolumeStatusAvailable

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVol, nil)
	mockAPI.EXPECT().VolumeExistsByMountPointName(ctx, cloneVolConfig.Name).Return(true, cloneVol, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, cloneVol, api.VolumeStatusAvailable, []string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
	assert.IsType(t, drivers.NewVolumeExistsError(""), result, "not VolumeExistsError")
}

func TestCreateClone_SnapshotNotFound(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, sourceVol, cloneVol, snapshot, _, _ := getStructsForCreateClone(ctx, driver, storagePool)
	cloneVolConfig.CloneSourceSnapshotInternal = snapshot.ID

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVol, nil).Times(1)
	mockAPI.EXPECT().VolumeExistsByMountPointName(ctx, cloneVol.CreationToken).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, sourceVol, snapshot.ID).Return(nil, errFailed).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
}

func TestCreateClone_SnapshotNotAvailable(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, sourceVol, cloneVol, snapshot, _, _ := getStructsForCreateClone(ctx, driver, storagePool)
	cloneVolConfig.CloneSourceSnapshotInternal = snapshot.ID
	snapshot.Status = api.SnapshotStatusError

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVol, nil).Times(1)
	mockAPI.EXPECT().VolumeExistsByMountPointName(ctx, cloneVol.CreationToken).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, sourceVol, snapshot.ID).Return(snapshot, nil).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
}

func TestCreateClone_SnapshotCreateFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, sourceVol, cloneVol, _, _, _ := getStructsForCreateClone(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVol, nil).Times(1)
	mockAPI.EXPECT().VolumeExistsByMountPointName(ctx, cloneVol.CreationToken).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CreateSnapshot(ctx, sourceVol, gomock.Any()).Return(nil, errFailed).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
}

func TestCreateClone_SnapshotWaitFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, sourceVolume, cloneVolume, snapshot, _, _ := getStructsForCreateClone(ctx,
		driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVolume, nil).Times(1)
	mockAPI.EXPECT().VolumeExistsByMountPointName(ctx, cloneVolume.CreationToken).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CreateSnapshot(ctx, sourceVolume, gomock.Any()).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().WaitForSnapshotStatus(ctx, sourceVolume, snapshot, api.SnapshotStatusAvailable, []string{api.SnapshotStatusError}, api.SnapshotTimeout).Return(errFailed).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
	assert.Equal(t, "", cloneVolConfig.InternalID, "internal ID set on volConfig")
}

/*
func TestCreateClone_SnapshotRefetchFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, sourceVol, cloneVol, snapshot, _, _ := getStructsForCreateClone(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVol, nil).Times(1)
	mockAPI.EXPECT().VolumeExistsByCreationToken(ctx, cloneVol.CreationToken).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CreateSnapshot(ctx, sourceVol, gomock.Any()).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().WaitForSnapshotStatus(ctx, sourceVol, snapshot, api.SnapshotStatusAvailable, []string{api.SnapshotStatusError}, api.SnapshotTimeout).Return(nil).Times(1)
	mockAPI.EXPECT().SnapshotByID(ctx, sourceVol, snapshot.ID).Return(nil, errFailed).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
    }
*/

/*
func TestCreateClone_InvalidLabel(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.Labels = map[string]string{
		"key1": "1234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890",
		"key2": "1234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890",
		"key3": "1234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890",
	}

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, _, sourceVol, cloneVol, snapshot, _, _ := getStructsForCreateClone(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVol, nil).Times(1)
	mockAPI.EXPECT().VolumeExistsByCreationToken(ctx, cloneVol.CreationToken).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().CreateSnapshot(ctx, sourceVol, gomock.Any()).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().WaitForSnapshotStatus(ctx, snapshot, sourceVol, api.SnapshotStatusAvailable, []string{api.SnapshotStatusError}, api.SnapshotTimeout).Return(nil).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
    }
*/

func TestCreateClone_CreateFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium
	driver.Config.NASType = "nfs"

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	storagePool := driver.pools["efs_pool"]

	sourceVolConfig, cloneVolConfig, createRequest, sourceVol, cloneVol, snapshot, exportRules, _ := getStructsForCreateClone(ctx, driver, storagePool)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, sourceVolConfig).Return(sourceVol, nil).Times(1)
	mockAPI.EXPECT().VolumeExistsByMountPointName(ctx, cloneVol.CreationToken).Return(false, nil, nil).Times(1)
	mockAPI.EXPECT().ExportRulesForVolume(ctx, sourceVol).Return(exportRules, nil).Times(1)
	mockAPI.EXPECT().CreateSnapshot(ctx, sourceVol, gomock.Any()).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().WaitForSnapshotStatus(ctx, sourceVol, snapshot, api.SnapshotStatusAvailable, []string{api.SnapshotStatusError}, api.SnapshotTimeout).Return(nil).Times(1)
	mockAPI.EXPECT().CreateVolume(ctx, createRequest).Return(nil, errFailed).Times(1)

	result := driver.CreateClone(ctx, sourceVolConfig, cloneVolConfig, nil)

	assert.Error(t, result, "expected error")
}

func TestCreateClone_ZoneSelectionSucceeds(t *testing.T) {
	// TODO: multi-az support
}

func getStructsForImport(ctx context.Context, driver *NASStorageDriver) (*storage.VolumeConfig, *api.Volume) {
	volConfig := &storage.VolumeConfig{
		Version:      "1",
		Name:         "testvol1",
		InternalName: "trident-testvol1",
	}

	volumeID := api.CreateVolumeID(BackendUUID, VolumeID)

	originalVolume := &api.Volume{
		ID:              volumeID,
		Name:            "importMe",
		Status:          api.VolumeStatusAvailable,
		CreationToken:   "importMe",
		Protocol:        api.ProtocolTypeNFS,
		SizeInGigabytes: VolumeSizeI64 / (1 << 30),
	}

	return volConfig, originalVolume
}

func TestImport_Managed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.NASType = "nfs"

	originalName := "importMe"

	volConfig, originalVol := getStructsForImport(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, originalName).Return(originalVol, nil).Times(1)
	mockAPI.EXPECT().EnsureVolumeInValidCapacityPool(ctx, originalVol).Return(nil).Times(1)

	result := driver.Import(ctx, volConfig, originalName)

	assert.NoError(t, result, "import failed")
	assert.Equal(t, originalName, volConfig.InternalName, "internal name mismatch")
	assert.Equal(t, originalVol.ID, volConfig.InternalID, "internal ID mismatch")
}

func TestImport_ManagedWithSnapshotDir(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.NASType = "nfs"

	originalName := "importMe"

	volConfig, originalVol := getStructsForImport(ctx, driver)

	volConfig.SnapshotDir = "true"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, originalName).Return(originalVol, nil).Times(1)
	mockAPI.EXPECT().EnsureVolumeInValidCapacityPool(ctx, originalVol).Return(nil).Times(1)

	result := driver.Import(ctx, volConfig, originalName)

	assert.NoError(t, result, "import failed")
	assert.Equal(t, originalName, volConfig.InternalName, "internal name mismatch")
	assert.Equal(t, originalVol.ID, volConfig.InternalID, "internal ID mismatch")
}

func TestImport_ManagedWithSnapshotDirFalse(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.NASType = "nfs"

	originalName := "importMe"

	volConfig, originalVol := getStructsForImport(ctx, driver)

	volConfig.SnapshotDir = "false"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, originalName).Return(originalVol, nil).Times(1)
	mockAPI.EXPECT().EnsureVolumeInValidCapacityPool(ctx, originalVol).Return(nil).Times(1)

	result := driver.Import(ctx, volConfig, originalName)

	assert.NoError(t, result, "import failed")
	assert.Equal(t, originalName, volConfig.InternalName, "internal name mismatch")
	assert.Equal(t, originalVol.ID, volConfig.InternalID, "internal ID mismatch")
}

func TestImport_ManagedWithInvalidSnapshotDirValue(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.NASType = "nfs"

	originalName := "importMe"

	volConfig, originalVol := getStructsForImport(ctx, driver)

	volConfig.SnapshotDir = "xxxffa"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, originalName).Return(originalVol, nil).Times(1)
	mockAPI.EXPECT().EnsureVolumeInValidCapacityPool(ctx, originalVol).Return(nil).Times(1)

	result := driver.Import(ctx, volConfig, originalName)

	assert.Error(t, result, "import succeeded")
	assert.NotNil(t, result, "received nil")
}

/*
func TestImport_ManagedWithLabels(t *testing.T) {
   }
*/

func TestImport_NotManaged(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	originalName := "importMe"

	volConfig, originalVol := getStructsForImport(ctx, driver)
	volConfig.ImportNotManaged = true

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, originalName).Return(originalVol, nil).Times(1)
	mockAPI.EXPECT().EnsureVolumeInValidCapacityPool(ctx, originalVol).Return(nil).Times(1)

	result := driver.Import(ctx, volConfig, originalName)

	assert.NoError(t, result, "import failed")
	assert.Equal(t, originalName, volConfig.InternalName, "internal name mismatch")
	assert.Equal(t, originalVol.ID, volConfig.InternalID, "internal ID mismatch")
}

func TestImport_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	originalName := "importMe"

	volConfig, _ := getStructsForImport(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result := driver.Import(ctx, volConfig, originalName)

	assert.Error(t, result, "expected error")
	assert.Equal(t, "trident-testvol1", volConfig.InternalName, "internal name mismatch")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestImport_NotFound(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	originalName := "importMe"

	volConfig, _ := getStructsForImport(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, originalName).Return(nil, errFailed).Times(1)

	result := driver.Import(ctx, volConfig, originalName)

	assert.Error(t, result, "expected error")
	assert.Equal(t, "trident-testvol1", volConfig.InternalName, "internal name mismatch")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestImport_InvalidVolumeStatus(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	originalName := "importMe"

	volConfig, originalVol := getStructsForImport(ctx, driver)
	originalVol.Status = api.VolumeStatusDeleting

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, originalName).Return(originalVol, nil).Times(1)

	result := driver.Import(ctx, volConfig, originalName)

	assert.Error(t, result, "expected error")
	assert.Equal(t, "trident-testvol1", volConfig.InternalName, "internal name mismatch")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestImport_InvalidCapacityPool(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	originalName := "importMe"

	volConfig, originalVol := getStructsForImport(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, originalName).Return(originalVol, nil).Times(1)
	mockAPI.EXPECT().EnsureVolumeInValidCapacityPool(ctx, originalVol).Return(errFailed).Times(1)

	result := driver.Import(ctx, volConfig, originalName)

	assert.Error(t, result, "expected error")
	assert.Equal(t, "trident-testvol1", volConfig.InternalName, "internal name mismatch")
	assert.Equal(t, "", volConfig.InternalID, "internal ID set on volConfig")
}

func TestImport_InvalidUnixPermissions(t *testing.T) {
	// TODO: (feat) UNIX permissions
}

func TestImport_ModifyVolumeFailed(t *testing.T) {
	// TODO: (feat) allow volume properties update (UNIX Permissions, Snapshot Dir, ...)
}

func TestImport_BackendVolumeMismatch(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	originalName := "importMe"

	volConfig, originalVol := getStructsForImport(ctx, driver)
	originalVol.Protocol = api.ProtocolTypeCIFS // Default d.Config.NASType == sa.NFS

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, originalName).Return(originalVol, nil).Times(1)
	mockAPI.EXPECT().EnsureVolumeInValidCapacityPool(ctx, originalVol).Return(nil).Times(1)

	result := driver.Import(ctx, volConfig, originalName)

	assert.Error(t, result, "import did not fail")
}

func TestRename(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	result := driver.Rename(ctx, "oldName", "newName")

	assert.Nil(t, result, "not nil")
}

func TestGetTelemetryLabels(t *testing.T) {
	_, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	result := driver.getTelemetryLabels(ctx)

	assert.True(t, strings.HasPrefix(result, `{"trident":{`))
}

func TestUpdateTelemetryLabels(t *testing.T) {
	// TODO: (feat) telemetry

	/*
		_, driver := newMockEFSDriver(t)
		driver.initializeTelemetry(ctx, BackendUUID)

		result := driver.updateTelemetryLabels(ctx, &api.Volume{})

		assert.NotNil(t, result)
		assert.True(t, strings.HasPrefix(result[drivers.TridentLabelTag], `{"trident":{`))
	*/
}

func TestWaitForVolumeCreate_Available(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volume := &api.Volume{
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusCreating,
	}

	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.SnapshotStatusAvailable, []string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)

	result := driver.waitForVolumeCreate(ctx, volume)

	assert.Nil(t, result)
}

func TestWaitForVolumeCreate_Creating(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volume := &api.Volume{
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusCreating,
	}

	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable, []string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.SnapshotStatusCreating, errFailed).Times(1)

	result := driver.waitForVolumeCreate(ctx, volume)

	assert.Error(t, result, "expected error")
	assert.IsType(t, errors.VolumeCreatingError(""), result, "not VolumeCreatingError")
}

func TestWaitForVolumeCreate_DeletingDelete(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volume := &api.Volume{
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusCreating,
	}

	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.SnapshotStatusAvailable, []string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusDeleting, errFailed).Times(1)

	result := driver.waitForVolumeCreate(ctx, volume)

	assert.NotNil(t, result, "not nil")
}

func TestWaitForVolumeCreate_ErrorDelete(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volume := &api.Volume{
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusCreating,
	}

	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable, []string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusError, errFailed).Times(1)
	mockAPI.EXPECT().DeleteVolume(ctx, volume).Return(nil).Times(1)

	result := driver.waitForVolumeCreate(ctx, volume)

	assert.NotNil(t, result, "nil error")
}

func TestWaitForVolumeCreate_ErrorDeleteFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volume := &api.Volume{
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusCreating,
	}

	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable, []string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(api.VolumeStatusError, errFailed).Times(1)
	mockAPI.EXPECT().DeleteVolume(ctx, volume).Return(errFailed).Times(1)

	result := driver.waitForVolumeCreate(ctx, volume)

	assert.NotNil(t, result, "nil error")
}

func TestWaitForVolumeCreate_OtherStates(t *testing.T) {
	for _, state := range []string{api.VolumeStatusExtending, api.VolumeStatusReverting, api.VolumeStatusShrinking, "unknown"} {

		mockAPI, driver := newMockEFSDriver(t)

		volume := &api.Volume{
			Name:          "testvol1",
			CreationToken: "pvc-testvol1",
			Status:        api.VolumeStatusCreating,
		}

		mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable, []string{api.VolumeStatusError}, driver.volumeCreateTimeout).Return(state, errFailed).Times(1)

		result := driver.waitForVolumeCreate(ctx, volume)

		assert.NotNil(t, result, "nil error")
	}
}

func TestWaitForExportRuleCreate(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volumeID := api.CreateVolumeID(BackendUUID, VolumeID)
	volume := &api.Volume{
		ID:            volumeID,
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusAvailable,
	}

	exportRule := &api.ExportRule{
		ID:          "",
		Status:      api.ExportRuleStatusApplying,
		AccessLevel: api.AccessReadWrite,
		AccessTo:    "1.1.1.1",
	}

	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, exportRule, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusActive, nil)

	result := driver.waitForExportRuleCreate(ctx, volume, exportRule)

	assert.Nil(t, result, "nil error")
}

func TestWaitForExportRuleCreate_QueuedToApply(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volumeID := api.CreateVolumeID(BackendUUID, VolumeID)
	volume := &api.Volume{
		ID:            volumeID,
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusAvailable,
	}

	exportRule := &api.ExportRule{
		ID:          "",
		Status:      api.ExportRuleStatusApplying,
		AccessLevel: api.AccessReadWrite,
		AccessTo:    "1.1.1.1",
	}

	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, exportRule, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusQueuedToApply, errFailed)

	result := driver.waitForExportRuleCreate(ctx, volume, exportRule)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, errors.VolumeCreatingError(errFailed.Error()), result, "expected volme creating error")
}

func TestWaitForExportRuleCreate_Applying(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volumeID := api.CreateVolumeID(BackendUUID, VolumeID)
	volume := &api.Volume{
		ID:            volumeID,
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusAvailable,
	}

	exportRule := &api.ExportRule{
		ID:          "",
		Status:      api.ExportRuleStatusApplying,
		AccessLevel: api.AccessReadWrite,
		AccessTo:    "1.1.1.1",
	}

	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, exportRule, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusApplying, errFailed)

	result := driver.waitForExportRuleCreate(ctx, volume, exportRule)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, errors.VolumeCreatingError(errFailed.Error()), result, "expected volme creating error")
}

func TestWaitForExportRuleCreate_Denying(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volumeID := api.CreateVolumeID(BackendUUID, VolumeID)
	volume := &api.Volume{
		ID:            volumeID,
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusAvailable,
	}

	exportRule := &api.ExportRule{
		ID:          "",
		Status:      api.ExportRuleStatusApplying,
		AccessLevel: api.AccessReadWrite,
		AccessTo:    "1.1.1.1",
	}

	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, exportRule, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusDenying, errFailed)

	result := driver.waitForExportRuleCreate(ctx, volume, exportRule)

	assert.NotNil(t, result, "expected error")
}

func TestWaitForExportRuleCreate_Error(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volumeID := api.CreateVolumeID(BackendUUID, VolumeID)
	volume := &api.Volume{
		ID:            volumeID,
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusAvailable,
	}

	exportRule := &api.ExportRule{
		ID:          "",
		Status:      api.ExportRuleStatusApplying,
		AccessLevel: api.AccessReadWrite,
		AccessTo:    "1.1.1.1",
	}

	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, exportRule, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusError, errFailed).Times(1)
	mockAPI.EXPECT().DeleteExportRule(ctx, volume, exportRule).Return(nil).Times(1)

	result := driver.waitForExportRuleCreate(ctx, volume, exportRule)

	assert.NotNil(t, result, "expected error")
}

func TestWaitForExportRuleCreate_Error_DeletingError(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volumeID := api.CreateVolumeID(BackendUUID, VolumeID)
	volume := &api.Volume{
		ID:            volumeID,
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusAvailable,
	}

	exportRule := &api.ExportRule{
		ID:          "",
		Status:      api.ExportRuleStatusApplying,
		AccessLevel: api.AccessReadWrite,
		AccessTo:    "1.1.1.1",
	}

	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, exportRule, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return(api.ExportRuleStatusError, errFailed).Times(1)
	mockAPI.EXPECT().DeleteExportRule(ctx, volume, exportRule).Return(errFailed).Times(1)

	result := driver.waitForExportRuleCreate(ctx, volume, exportRule)

	assert.NotNil(t, result, "expected error")
}

func TestWaitForExportRuleCreate_OtherStatus(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volumeID := api.CreateVolumeID(BackendUUID, VolumeID)
	volume := &api.Volume{
		ID:            volumeID,
		Name:          "testvol1",
		CreationToken: "pvc-testvol1",
		Status:        api.VolumeStatusAvailable,
	}

	exportRule := &api.ExportRule{
		ID:          "",
		Status:      api.ExportRuleStatusApplying,
		AccessLevel: api.AccessReadWrite,
		AccessTo:    "1.1.1.1",
	}

	mockAPI.EXPECT().WaitForExportRuleStatus(ctx, volume, exportRule, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, driver.volumeCreateTimeout).Return("unknownStatus", errFailed).Times(1)

	result := driver.waitForExportRuleCreate(ctx, volume, exportRule)

	assert.NotNil(t, result, "expected error")
}

func getStructsForDestroyNFSVolume(ctx context.Context, driver *NASStorageDriver) (volConfig *storage.VolumeConfig, volume, srcVolume *api.Volume, snapshot *api.Snapshot) {
	volumeID := api.CreateVolumeID(BackendUUID, "02d3de3a-570c-444e-be48-d3325bf84e37")
	srcVolumeID := api.CreateVolumeID(BackendUUID, VolumeID)
	snapshotID := api.CreateSnapshotID(BackendUUID, VolumeID, SnapshotID)

	volConfig = &storage.VolumeConfig{
		Version:      "1",
		Name:         "tesvol1",
		InternalName: "trident-testvol1",
		Size:         VolumeSizeStr,
		InternalID:   volumeID,
	}

	labels := make(map[string]string)
	labels[drivers.TridentLabelTag] = driver.getTelemetryLabels(ctx)
	labels[storage.ProvisioningLabelTag] = ""

	volume = &api.Volume{
		ID:              volumeID,
		ServiceID:       BackendUUID,
		Name:            "testvol1",
		Protocol:        api.ProtocolTypeNFS,
		SizeInGigabytes: VolumeSizeI64 / (1 << 30),
		CreationToken:   "trident-testvol1",
		Status:          api.VolumeStatusAvailable,
	}

	srcVolume = &api.Volume{
		ID:              srcVolumeID,
		ServiceID:       BackendUUID,
		Name:            "testsrcvol1",
		Protocol:        api.ProtocolTypeNFS,
		SizeInGigabytes: VolumeSizeI64 / (1 << 30),
		CreationToken:   "trident-testsrcvol1",
		Status:          api.VolumeStatusAvailable,
	}

	snapshot = &api.Snapshot{
		ID:     snapshotID,
		Name:   "snap1",
		Status: api.SnapshotStatusAvailable,
	}

	return volConfig, volume, srcVolume, snapshot
}

func TestDestroy_NFSVolume_Docker(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.DriverContext = tridentconfig.ContextDocker

	volConfig, volume, _, _ := getStructsForDestroyNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().DeleteVolume(ctx, volume).Return(nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusDeleted, []string{api.VolumeStatusError}, driver.defaultTimeout()).Return("", nil).Times(1)

	result := driver.Destroy(ctx, volConfig)

	assert.Nil(t, result, "not nil")
}

func TestDestroy_NFSVolume_CSI(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.DriverContext = tridentconfig.ContextCSI

	volConfig, volume, _, _ := getStructsForDestroyNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().DeleteVolume(ctx, volume).Return(nil).Times(1)

	result := driver.Destroy(ctx, volConfig)

	assert.Nil(t, result, "not nil")
}

func TestDestroy_Clone_DeleteAutomaticSnapshot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.DriverContext = tridentconfig.ContextCSI

	volConfig, volume, srcVolume, snapshot := getStructsForDestroyNFSVolume(ctx, driver)
	volConfig.CloneSourceVolumeInternal = "testsrcvol1"
	volConfig.CloneSourceSnapshotInternal = snapshot.ID

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().DeleteVolume(ctx, volume).Return(nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusDeleted, []string{api.VolumeStatusError}, driver.defaultTimeout()).Return(api.VolumeStatusDeleted, nil).Times(1)
	mockAPI.EXPECT().VolumeByID(ctx, srcVolume.ID).Return(srcVolume, nil).Times(1)
	mockAPI.EXPECT().SnapshotByID(ctx, srcVolume, volConfig.CloneSourceSnapshotInternal).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().DeleteSnapshot(ctx, srcVolume, snapshot).Return(nil).Times(1)

	err := driver.Destroy(ctx, volConfig)

	assert.Nil(t, err, "not nil")
}

func TestDestroy_Clone_DeleteAutomaticSnapshot_VolDeletingState(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.DriverContext = tridentconfig.ContextCSI

	volConfig, volume, srcVolume, snapshot := getStructsForDestroyNFSVolume(ctx, driver)
	volConfig.CloneSourceVolumeInternal = "testsrcvol1"
	volConfig.CloneSourceSnapshotInternal = snapshot.ID
	volume.Status = api.VolumeStatusDeleting

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusDeleted, []string{api.VolumeStatusError}, driver.defaultTimeout()).Return(api.VolumeStatusDeleted, nil).Times(1)
	mockAPI.EXPECT().DeleteVolume(ctx, gomock.Any()).Times(0)
	mockAPI.EXPECT().VolumeByID(ctx, srcVolume.ID).Return(srcVolume, nil).Times(1)
	mockAPI.EXPECT().SnapshotByID(ctx, srcVolume, volConfig.CloneSourceSnapshotInternal).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().DeleteSnapshot(ctx, srcVolume, snapshot).Return(nil).Times(1)

	err := driver.Destroy(ctx, volConfig)

	assert.Nil(t, err, "not nil")
}

func TestDestroy_Clone_DeleteAutomaticSnapshot_VolDeleteError(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.DriverContext = tridentconfig.ContextCSI

	volConfig, _, _, snapshot := getStructsForDestroyNFSVolume(ctx, driver)
	volConfig.CloneSourceVolumeInternal = "testsrcvol1"
	volConfig.CloneSourceSnapshotInternal = snapshot.ID

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, errors.NotFoundError("volume not found error")).Times(1)
	mockAPI.EXPECT().DeleteVolume(ctx, gomock.Any()).Times(0)
	mockAPI.EXPECT().DeleteSnapshot(ctx, gomock.Any(), gomock.Any()).Times(0)

	err := driver.Destroy(ctx, volConfig)

	assert.NotNil(t, err)
}

func TestDestroy_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, _, _, _ := getStructsForDestroyNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result := driver.Destroy(ctx, volConfig)

	assert.NotNil(t, result, "expected error")
}

func TestDestroy_VolumeExistsCheckFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, _, _, _ := getStructsForDestroyNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, errFailed).Times(1)

	result := driver.Destroy(ctx, volConfig)

	assert.NotNil(t, result, "expect error")
}

func TestDestroy_AlreadyDeleted(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, _, _, _ := getStructsForDestroyNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)

	result := driver.Destroy(ctx, volConfig)

	assert.Nil(t, result, "not nil")
}

func TestDestroy_StillDeletingDeleted_Docker(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.DriverContext = tridentconfig.ContextDocker

	volConfig, volume, _, _ := getStructsForDestroyNFSVolume(ctx, driver)
	volume.Status = api.VolumeStatusDeleting

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusDeleted, []string{api.VolumeStatusError}, tridentconfig.DockerDefaultTimeout).Return(api.VolumeStatusDeleted, nil).Times(1)

	result := driver.Destroy(ctx, volConfig)

	assert.Nil(t, result, "not nil")
}

func TestDestroy_StillDeleting_Docker(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.DriverContext = tridentconfig.ContextDocker

	volConfig, volume, _, _ := getStructsForDestroyNFSVolume(ctx, driver)
	volume.Status = api.VolumeStatusDeleting

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusDeleted, []string{api.VolumeStatusError}, tridentconfig.DockerDefaultTimeout).Return(api.SnapshotStatusDeleting, errFailed).Times(1)

	result := driver.Destroy(ctx, volConfig)

	assert.NotNil(t, result, "expected error")
}

func TestDestroy_StillDeleting_CSI(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.DriverContext = tridentconfig.ContextCSI

	volConfig, volume, _, _ := getStructsForDestroyNFSVolume(ctx, driver)
	volume.Status = api.VolumeStatusDeleting

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)

	result := driver.Destroy(ctx, volConfig)

	assert.Nil(t, result, "expected error")
}

func TestDestroy_DeleteFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, volume, _, _ := getStructsForDestroyNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().DeleteVolume(ctx, volume).Return(errFailed).Times(1)

	result := driver.Destroy(ctx, volConfig)

	assert.NotNil(t, result, "expected error")
}

func TestDestroy_VolumeWaitFailed_Docker(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.DriverContext = tridentconfig.ContextDocker

	volConfig, volume, _, _ := getStructsForDestroyNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().DeleteVolume(ctx, volume).Return(nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusDeleted, []string{api.VolumeStatusError}, driver.defaultTimeout()).Return(api.VolumeStatusDeleting, errFailed).Times(1)

	result := driver.Destroy(ctx, volConfig)

	assert.NotNil(t, result, "expected error")
}

func getStructsForDeleteAutomaticSnapshot(ctx context.Context, driver *NASStorageDriver) (
	srcVolume *api.Volume, cloneVolumeConfig *storage.VolumeConfig,
	cloneVolume *api.Volume, snapshot *api.Snapshot,
) {
	srcVolumeID := api.CreateVolumeID(BackendUUID, VolumeID)
	cloneVolumeID := api.CreateVolumeID(BackendUUID, "5fc372ad-b068-4b09-9faa-1b61d10f04be")
	snapshotID := api.CreateSnapshotID(BackendUUID, VolumeID, SnapshotID)

	cloneVolumeConfig = &storage.VolumeConfig{
		Version:                     "1",
		Name:                        "testvol1",
		InternalName:                "trident-testvol1",
		Size:                        VolumeSizeStr,
		InternalID:                  cloneVolumeID,
		CloneSourceSnapshotInternal: snapshotID,
		CloneSourceVolume:           "testsrcvol1",
		CloneSourceVolumeInternal:   "testsrcvol1",
	}

	cloneVolume = &api.Volume{
		ID:              cloneVolumeID,
		Name:            "testvol1",
		Status:          api.VolumeStatusAvailable,
		SizeInGigabytes: VolumeSizeI64 / (1 << 30),
	}

	srcVolume = &api.Volume{
		ID:              srcVolumeID,
		Name:            "testsrcvol1",
		Status:          api.VolumeStatusAvailable,
		SizeInGigabytes: VolumeSizeI64 / (1 << 30),
	}

	snapshot = &api.Snapshot{
		Name:   "snap1",
		ID:     snapshotID,
		Status: api.SnapshotStatusAvailable,
	}

	return srcVolume, cloneVolumeConfig, cloneVolume, snapshot
}

func TestDeleteAutomaticSnapshot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	srcVol, cloneVolConfig, _, snapshot := getStructsForDeleteAutomaticSnapshot(ctx, driver)

	mockAPI.EXPECT().VolumeByID(ctx, srcVol.ID).Return(srcVol, nil).Times(1)
	mockAPI.EXPECT().SnapshotByID(ctx, srcVol, snapshot.ID).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().DeleteSnapshot(ctx, srcVol, snapshot).Return(nil).Times(1)

	driver.deleteAutomaticSnapshot(ctx, nil, cloneVolConfig)
}

func TestDeleteAutomaticSnapshot_NoSnapshot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	_, cloneVolConfig, _, _ := getStructsForDeleteAutomaticSnapshot(ctx, driver)
	cloneVolConfig.CloneSourceSnapshotInternal = ""

	mockAPI.EXPECT().VolumeByID(ctx, gomock.Any()).Times(0)
	mockAPI.EXPECT().SnapshotForVolume(ctx, gomock.Any(), gomock.Any()).Times(0)
	mockAPI.EXPECT().DeleteSnapshot(ctx, gomock.Any(), gomock.Any()).Times(0)

	driver.deleteAutomaticSnapshot(ctx, nil, cloneVolConfig)
}

func TestDeleteAutomaticSnapshot_ExistingSrcSnapshot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	_, cloneVolConfig, _, _ := getStructsForDeleteAutomaticSnapshot(ctx, driver)
	cloneVolConfig.CloneSourceSnapshot = cloneVolConfig.CloneSourceSnapshotInternal

	mockAPI.EXPECT().VolumeByID(ctx, gomock.Any()).Times(0)
	mockAPI.EXPECT().SnapshotForVolume(ctx, gomock.Any(), gomock.Any()).Times(0)
	mockAPI.EXPECT().DeleteSnapshot(ctx, gomock.Any(), gomock.Any()).Times(0)

	driver.deleteAutomaticSnapshot(ctx, nil, cloneVolConfig)
}

func TestDeleteAutomaticSnapshot_VolDeleteError(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	_, cloneVolConfig, _, _ := getStructsForDeleteAutomaticSnapshot(ctx, driver)

	mockAPI.EXPECT().VolumeByID(ctx, gomock.Any()).Times(0)
	mockAPI.EXPECT().SnapshotForVolume(ctx, gomock.Any(), gomock.Any()).Times(0)
	mockAPI.EXPECT().DeleteSnapshot(ctx, gomock.Any(), gomock.Any()).Times(0)

	driver.deleteAutomaticSnapshot(ctx, errors.New("volume delete error"), cloneVolConfig)
}

func TestDeleteAutomaticSnapshot_VolByIDError(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	srcVol, cloneVolConfig, _, _ := getStructsForDeleteAutomaticSnapshot(ctx, driver)

	tests := []struct {
		name            string
		volumeByIDError error
	}{
		{
			name:            "Volume not found",
			volumeByIDError: errors.NotFoundError("not found"),
		},
		{
			name:            "Volume retrieval error",
			volumeByIDError: errFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mockAPI.EXPECT().VolumeByID(ctx, srcVol.ID).Return(nil, test.volumeByIDError).Times(1)
			mockAPI.EXPECT().SnapshotByID(ctx, srcVol, cloneVolConfig.CloneSourceSnapshotInternal).Times(0)
			mockAPI.EXPECT().DeleteSnapshot(ctx, gomock.Any(), gomock.Any()).Times(0)

			driver.deleteAutomaticSnapshot(ctx, nil, cloneVolConfig)
		})
	}
}

func TestDeleteAutomaticSnapshot_SnapshotByIDError(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	srcVol, cloneVolConfig, _, _ := getStructsForDeleteAutomaticSnapshot(ctx, driver)

	tests := []struct {
		name                   string
		snapshotForVolumeError error
	}{
		{
			name:                   "Snapshot not found",
			snapshotForVolumeError: errors.NotFoundError("not found"),
		},
		{
			name:                   "Snapshot retrieval error",
			snapshotForVolumeError: errFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mockAPI.EXPECT().VolumeByID(ctx, srcVol.ID).Return(srcVol, nil).Times(1)
			mockAPI.EXPECT().SnapshotByID(ctx, srcVol, cloneVolConfig.CloneSourceSnapshotInternal).Return(nil, test.snapshotForVolumeError).Times(1)
			mockAPI.EXPECT().DeleteSnapshot(ctx, gomock.Any(), gomock.Any()).Times(0)

			driver.deleteAutomaticSnapshot(ctx, nil, cloneVolConfig)
		})
	}
}

func TestDeleteAutomaticSnapshot_DeleteSnapshotError(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	srcVol, cloneVolConfig, _, snapshot := getStructsForDeleteAutomaticSnapshot(ctx, driver)
	mockAPI.EXPECT().VolumeByID(ctx, srcVol.ID).Return(srcVol, nil).Times(1)
	mockAPI.EXPECT().SnapshotByID(ctx, srcVol, cloneVolConfig.CloneSourceSnapshotInternal).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().DeleteSnapshot(ctx, srcVol, snapshot).Return(errors.New("delete failed")).Times(1)

	driver.deleteAutomaticSnapshot(ctx, nil, cloneVolConfig)
}

func getStructsForPublishNFSVolume(ctx context.Context, driver *NASStorageDriver) (*storage.VolumeConfig, *api.Volume, []*api.AccessPath, *models.VolumePublishInfo) {
	volumeID := api.CreateVolumeID(BackendUUID, VolumeID)

	volConfig := &storage.VolumeConfig{
		Version:      "1",
		Name:         "testvol1",
		InternalName: "trident-testvol1",
		Size:         VolumeSizeStr,
		InternalID:   volumeID,
		AccessInfo: models.VolumeAccessInfo{
			NfsAccessInfo: models.NfsAccessInfo{
				NfsPath: "/trident-testvol1",
			},
		},
	}

	accessPaths := []*api.AccessPath{
		{
			Path: "1.1.1.1:/trident-testvol1",
		},
	}

	volume := &api.Volume{
		ID:              volumeID,
		Status:          api.VolumeStatusAvailable,
		CreationToken:   "trident-testvol1",
		Protocol:        api.ProtocolTypeNFS,
		SizeInGigabytes: VolumeSizeI64 / (1 << 30),
	}

	publishInfo := &models.VolumePublishInfo{}

	return volConfig, volume, accessPaths, publishInfo
}

func TestPublish_NFSVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.NASType = sa.NFS

	volConfig, volume, accessPaths, publishInfo := getStructsForPublishNFSVolume(ctx, driver)
	publishInfo.NfsPath = volConfig.AccessInfo.NfsPath

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().VolumeAccessPaths(ctx, volume).Return(accessPaths, nil).Times(1)

	result := driver.Publish(ctx, volConfig, publishInfo)

	assert.Nil(t, result, "not nil")
	assert.Equal(t, "/trident-testvol1", publishInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "1.1.1.1", publishInfo.NfsServerIP, "NFS server IP mismatch")
	assert.Equal(t, "nfs", publishInfo.FilesystemType, "filesystem type mismatch")
	assert.Equal(t, "rw,hard,rsize=65536,wsize=65536,nfsvers=3,tcp", publishInfo.MountOptions, "mount options mismatch")
}

func TestPublish_ROClone_NFSVolume(t *testing.T) {
	// TODO: (feat) RO clone support
}

func TestPublish_SMBVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.NASType = sa.SMB

	volConfig, volume, accessPaths, publishInfo := getStructsForPublishNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().VolumeAccessPaths(ctx, volume).Return(accessPaths, nil).Times(1)

	result := driver.Publish(ctx, volConfig, publishInfo)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, "", publishInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "", publishInfo.NfsServerIP, "NFS server IP mismatch")
	assert.Equal(t, "", publishInfo.FilesystemType, "filesystem type mismatch")
	assert.Equal(t, "", publishInfo.MountOptions, "mount options mismatch")
}

func TestPublish_MountOptions(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.NASType = "nfs"

	volConfig, volume, accessPaths, publishInfo := getStructsForPublishNFSVolume(ctx, driver)
	volConfig.MountOptions = "nfsvers=4.1"

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().VolumeAccessPaths(ctx, volume).Return(accessPaths, nil).Times(1)

	result := driver.Publish(ctx, volConfig, publishInfo)

	assert.Nil(t, result, "not nil")
	assert.Equal(t, "/trident-testvol1", publishInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "1.1.1.1", publishInfo.NfsServerIP, "NFS server IP mismatch")
	assert.Equal(t, "nfs", publishInfo.FilesystemType, "filesystem type mismatch")
	assert.Equal(t, "nfsvers=4.1", publishInfo.MountOptions, "mount options mismatch")
}

func TestPublish_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, _, _, publishInfo := getStructsForPublishNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result := driver.Publish(ctx, volConfig, publishInfo)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, "", publishInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "", publishInfo.NfsServerIP, "NFS server IP mismatch")
	assert.Equal(t, "", publishInfo.FilesystemType, "filesystem type mismatch")
	assert.Equal(t, "", publishInfo.MountOptions, "mount options mismatch")
}

func TestPublish_NonexistentVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, _, _, publishInfo := getStructsForPublishNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(nil, errFailed).Times(1)

	result := driver.Publish(ctx, volConfig, publishInfo)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, "", publishInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "", publishInfo.NfsServerIP, "NFS server IP mismatch")
	assert.Equal(t, "", publishInfo.FilesystemType, "filesystem type mismatch")
	assert.Equal(t, "", publishInfo.MountOptions, "mount options mismatch")
}

func TestPublish_ROClone_NonexistentVolume(t *testing.T) {
	// TODO: (feat) RO clone support
}

func TestPublish_VolumeAccessPathsFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, volume, _, publishInfo := getStructsForPublishNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().VolumeAccessPaths(ctx, volume).Return(nil, errFailed).Times(1)

	result := driver.Publish(ctx, volConfig, publishInfo)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, "", publishInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "", publishInfo.NfsServerIP, "NFS server IP mismatch")
	assert.Equal(t, "", publishInfo.FilesystemType, "filesystem type mismatch")
	assert.Equal(t, "", publishInfo.MountOptions, "mount options mismatch")
}

func TestPublish_NoMountTarget(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, volume, accessPaths, publishInfo := getStructsForPublishNFSVolume(ctx, driver)
	accessPaths = []*api.AccessPath{}

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().VolumeAccessPaths(ctx, volume).Return(accessPaths, nil).Times(1)

	result := driver.Publish(ctx, volConfig, publishInfo)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, "", publishInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "", publishInfo.NfsServerIP, "NFS server IP mismatch")
	assert.Equal(t, "", publishInfo.FilesystemType, "filesystem type mismatch")
	assert.Equal(t, "", publishInfo.MountOptions, "mount options mismatch")
}

func getStructsForCreateSnapshot(ctx context.Context, driver *NASStorageDriver, snapTime time.Time) (
	*storage.VolumeConfig, *api.Volume, *storage.SnapshotConfig, *api.Snapshot,
) {
	volumeID := api.CreateVolumeID(BackendUUID, VolumeID)

	volConfig := &storage.VolumeConfig{
		Version:      "1",
		Name:         "testvol1",
		InternalName: "trident-testvol1",
		Size:         VolumeSizeStr,
		InternalID:   volumeID,
	}

	volume := &api.Volume{
		ID:              volumeID,
		Name:            "testvol1",
		CreationToken:   "trident-testvol1",
		Protocol:        api.ProtocolTypeNFS,
		SizeInGigabytes: 100,
		Status:          api.VolumeStatusAvailable,
	}

	snapConfig := &storage.SnapshotConfig{
		Version:            "1",
		Name:               "snap1",
		InternalName:       "snap1",
		VolumeName:         "testvol1",
		VolumeInternalName: "trident-testvol1",
	}

	snapshot := &api.Snapshot{
		// ID
		// Volume info ?
		CreatedAt: snapTime,
		Status:    api.SnapshotStatusAvailable,
	}

	return volConfig, volume, snapConfig, snapshot
}

func TestCanSnapshot(t *testing.T) {
	_, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()

	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	result := driver.CanSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
}

func TestGetSnaphsot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, snapshot := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(snapshot, nil).Times(1)

	result, err := driver.GetSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, err, "not nil")
	assert.Equal(t, snapConfig, result.Config, "snapshot mismatch")
	assert.Equal(t, int64(0), result.SizeBytes, "snapshot size mismatch")
	assert.Equal(t, storage.SnapshotStateOnline, result.State, "snapshot state mismatch")
}

func TestGetSnapshot_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result, err := driver.GetSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func TestGetSnapshot_VolumeExistsCheckFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, errFailed).Times(1)

	result, err := driver.GetSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func TestGetSnapshot_NonexistentVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)

	result, err := driver.GetSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
	assert.Nil(t, err, "not nil")
}

func TestGetSnapshot_NonexistentSnapshot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(nil,
		errors.NotFoundError("not found")).Times(1)

	result, err := driver.GetSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
	assert.Nil(t, err, "not nil")
}

func TestGetSnapshot_GetSnapshotFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(nil,
		errFailed).Times(1)

	result, err := driver.GetSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func TestGetSnapshot_SnapshotNotAvailable(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, snapshot := getStructsForCreateSnapshot(ctx, driver, snapTime)
	snapshot.Status = api.SnapshotStatusError

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(snapshot, nil).Times(1)

	result, err := driver.GetSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func getSnapshotsForList(snapTime time.Time) *[]*api.Snapshot {
	return &[]*api.Snapshot{
		{
			// ID
			// Volume info ?
			Name:      "snap1",
			CreatedAt: snapTime,
			Status:    api.SnapshotStatusAvailable,
		},
		{
			// ID
			// Volume info ?
			Name:      "snap2",
			CreatedAt: snapTime,
			Status:    api.SnapshotStatusAvailable,
		},
		{
			// ID
			// Volume info ?
			Name:      "snap3",
			CreatedAt: snapTime,
			Status:    api.SnapshotStatusError,
		},
	}
}

func TestGetSnapshots(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, _, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)
	snapshots := getSnapshotsForList(snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotsForVolume(ctx, volume).Return(snapshots, nil).Times(1)

	result, err := driver.GetSnapshots(ctx, volConfig)

	expectedSnap0 := &storage.Snapshot{
		Config: &storage.SnapshotConfig{
			Version:            tridentconfig.OrchestratorAPIVersion,
			Name:               "snap1",
			InternalName:       "snap1",
			VolumeName:         "testvol1",
			VolumeInternalName: "trident-testvol1",
		},
		Created:   snapTime.UTC().Format(convert.TimestampFormat),
		SizeBytes: 0,
		State:     storage.SnapshotStateOnline,
	}

	expectedSnap1 := &storage.Snapshot{
		Config: &storage.SnapshotConfig{
			Version:            tridentconfig.OrchestratorAPIVersion,
			Name:               "snap2",
			InternalName:       "snap2",
			VolumeName:         "testvol1",
			VolumeInternalName: "trident-testvol1",
		},
		Created:   snapTime.UTC().Format(convert.TimestampFormat),
		SizeBytes: 0,
		State:     storage.SnapshotStateOnline,
	}

	assert.Nil(t, err, "not nil")
	assert.Len(t, result, 2)

	assert.Equal(t, result[0], expectedSnap0, "snapshot 0 mismatch")
	assert.Equal(t, result[1], expectedSnap1, "snapshot 1 mismatch")
}

func TestGetSnapshots_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, _, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result, err := driver.GetSnapshots(ctx, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func TestGetSnapshots_NonexistentVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, _, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(nil, errFailed).Times(1)

	result, err := driver.GetSnapshots(ctx, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func TestGetSnapshots_GetSnapshotsFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, _, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotsForVolume(ctx, volume).Return(nil, errFailed).Times(1)

	result, err := driver.GetSnapshots(ctx, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func TestCreateSnapshot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, snapshot := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().CreateSnapshot(ctx, volume, snapConfig.InternalName).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().WaitForSnapshotStatus(ctx, volume, snapshot, api.SnapshotStatusAvailable, []string{api.SnapshotStatusError},
		api.SnapshotTimeout).Return(nil).Times(1)

	result, err := driver.CreateSnapshot(ctx, snapConfig, volConfig)

	expectedSnapshot := &storage.Snapshot{
		Config:    snapConfig,
		Created:   snapTime.UTC().Format(convert.TimestampFormat),
		SizeBytes: 0,
		State:     storage.SnapshotStateOnline,
	}

	assert.Nil(t, err, "not nil")
	assert.Equal(t, expectedSnapshot, result)
}

func TestCreateSnapshot_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result, err := driver.CreateSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func TestCreateSnapshot_VolumeExistsCheckFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, errFailed).Times(1)

	result, err := driver.CreateSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func TestCreateSnapshot_NonexistentVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)

	result, err := driver.CreateSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func TestCreateSnapshot_SnapshotCreateFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().CreateSnapshot(ctx, volume, snapConfig.InternalName).Return(nil, errFailed).Times(1)

	result, err := driver.CreateSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func TestCreateSnapshot_SnapshotWaitFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, snapshot := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().CreateSnapshot(ctx, volume, snapConfig.InternalName).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().WaitForSnapshotStatus(ctx, volume, snapshot, api.SnapshotStatusAvailable, []string{api.SnapshotStatusError},
		api.SnapshotTimeout).Return(errFailed).Times(1)

	result, err := driver.CreateSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "expected error")
}

func TestRestoreSnapshot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, snapshot := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().RestoreSnapshot(ctx, volume, snapshot).Return(nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusError, api.VolumeStatusRevertingError, api.VolumeStatusDeleting, api.VolumeStatusDeleted}, api.DefaultTimeout).Return(api.VolumeStatusAvailable, nil).Times(1)

	result := driver.RestoreSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
}

func TestRestoreSnapshot_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result := driver.RestoreSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func TestRestoreSnapshot_VolumeExistsCheckFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(nil, errFailed).Times(1)

	result := driver.RestoreSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func TestRestoreSnapshot_NonexistentVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(nil, errors.NotFoundError("not found")).Times(1)

	result := driver.RestoreSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func TestRestoreSnapshot_NonexistentSnapshot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(nil, errors.NotFoundError("not found")).Times(1)

	result := driver.RestoreSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func TestRestoreSnapshot_GetSnapshotFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(nil, errFailed).Times(1)

	result := driver.RestoreSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func TestRestoreSnapshot_SnapshotRestoreFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, snapshot := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().RestoreSnapshot(ctx, volume, snapshot).Return(errFailed).Times(1)

	result := driver.RestoreSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func TestRestoreSnapshot_VolumeWaitFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, snapshot := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().RestoreSnapshot(ctx, volume, snapshot).Return(nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusError, api.VolumeStatusRevertingError, api.VolumeStatusDeleting, api.VolumeStatusDeleted}, api.DefaultTimeout).Return("", errFailed).Times(1)

	result := driver.RestoreSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func TestDeleteSnapshot_Docker(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.DriverContext = tridentconfig.ContextDocker

	snapTime := time.Now()
	volConfig, volume, snapConfig, snapshot := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().DeleteSnapshot(ctx, volume, snapshot).Return(nil).Times(1)
	mockAPI.EXPECT().WaitForSnapshotStatus(ctx, volume, snapshot, api.SnapshotStatusDeleted, []string{api.SnapshotStatusError, api.SnapshotStatusErrorDeleting}, api.SnapshotTimeout).Return(nil).Times(1)

	result := driver.DeleteSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
}

func TestDeleteSnapshot_CSI(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, snapshot := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().DeleteSnapshot(ctx, volume, snapshot).Return(nil).Times(1)
	mockAPI.EXPECT().WaitForSnapshotStatus(ctx, volume, snapshot, api.SnapshotStatusDeleted, []string{api.SnapshotStatusError, api.SnapshotStatusErrorDeleting}, api.SnapshotTimeout).Return(nil).Times(1)

	result := driver.DeleteSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
}

func TestDeleteSnapshot_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result := driver.DeleteSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func TestDeleteSnapshot_VolumeExistsCheckFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, errFailed).Times(1)

	result := driver.DeleteSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func TestDeleteSnapshot_NonexistentVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, _, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(false, nil, nil).Times(1)

	result := driver.DeleteSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
}

func TestDeleteSnapshot_NonexistentSnapshot(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(nil, errors.NotFoundError("not found")).Times(1)

	result := driver.DeleteSnapshot(ctx, snapConfig, volConfig)

	assert.Nil(t, result, "not nil")
}

func TestDeleteSnapshot_GetSnapshotFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, _ := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(nil, errFailed).Times(1)

	result := driver.DeleteSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func TestDeleteSnapshot_SnapshotDeleteFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, snapshot := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().DeleteSnapshot(ctx, volume, snapshot).Return(errFailed).Times(1)

	result := driver.DeleteSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func TestDeleteSnapshot_SnapshotWaitFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	snapTime := time.Now()
	volConfig, volume, snapConfig, snapshot := getStructsForCreateSnapshot(ctx, driver, snapTime)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeExists(ctx, volConfig).Return(true, volume, nil).Times(1)
	mockAPI.EXPECT().SnapshotForVolume(ctx, volume, snapConfig.InternalName).Return(snapshot, nil).Times(1)
	mockAPI.EXPECT().DeleteSnapshot(ctx, volume, snapshot).Return(nil).Times(1)
	mockAPI.EXPECT().WaitForSnapshotStatus(ctx, volume, snapshot, api.SnapshotStatusDeleted, []string{api.SnapshotStatusError, api.SnapshotStatusErrorDeleting}, api.SnapshotTimeout).Return(errFailed).Times(1)

	result := driver.DeleteSnapshot(ctx, snapConfig, volConfig)

	assert.Error(t, result, "expected error")
}

func getVolumesForList() []*api.Volume {
	return []*api.Volume{
		{
			Status:        api.VolumeStatusAvailable,
			CreationToken: "myPrefix-testvol1",
		},
		{
			Status:        api.VolumeStatusAvailable,
			CreationToken: "myPrefix-testvol2",
		},
		{
			Status:        api.VolumeStatusDeleting,
			CreationToken: "myPrefix-testvol3",
		},

		{
			Status:        api.VolumeStatusError,
			CreationToken: "myPrefix-testvol4",
		},
		{
			Status:        api.VolumeStatusAvailable,
			CreationToken: "testvol5",
		},
	}
}

func TestList(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	storagePrefix := "myPrefix-"
	driver.Config.StoragePrefix = &storagePrefix

	volumes := getVolumesForList()

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volumes(ctx).Return(volumes, nil).Times(1)

	list, result := driver.List(ctx)

	assert.Nil(t, result, "expected no error")
	assert.Equal(t, []string{"testvol1", "testvol2"}, list, "expected different output")
}

func TestList_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	list, result := driver.List(ctx)

	assert.Error(t, result, "expected error")
	assert.Nil(t, list, "list not nil")
}

func TestList_ListFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volumes(ctx).Return(nil, errFailed).Times(1)

	list, result := driver.List(ctx)

	assert.Error(t, result, "expected error")
	assert.Nil(t, list, "list not nil")
}

func TestList_ListNone(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volumes(ctx).Return([]*api.Volume{}, nil).Times(1)

	list, result := driver.List(ctx)

	assert.Nil(t, result, "expected nil")
	assert.Equal(t, []string{}, list, "list not empty")
}

func TestGet(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	volume := &api.Volume{}

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, "pvc-testvol1").Return(volume, nil).Times(1)

	volConfig := &storage.VolumeConfig{
		Name:         "tesvol1",
		InternalName: "pvc-testvol1",
	}

	result := driver.Get(ctx, volConfig)

	assert.NoError(t, result, "expect no error")
}

func TestGet_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	volConfig := &storage.VolumeConfig{
		Name:         "tesvol1",
		InternalName: "pvc-testvol1",
	}

	result := driver.Get(ctx, volConfig)

	assert.Error(t, result, "expected error")
}

func TestGet_NotFound(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, "pvc-testvol1").Return(nil, errors.NotFoundError("not found")).Times(1)

	volConfig := &storage.VolumeConfig{
		Name:         "tesvol1",
		InternalName: "pvc-testvol1",
	}

	result := driver.Get(ctx, volConfig)

	assert.Error(t, result, "expected error")
}

func getStructsForResizeVolume(ctx context.Context, driver *NASStorageDriver) (*storage.VolumeConfig, *api.Volume) {
	volumeID := api.CreateVolumeID("dc114541-f3a7-4767-a9a4-cb7e58c60a82", "2ac741dd-3438-4643-b4b0-879251f92cf4")

	volConfig := &storage.VolumeConfig{
		Version:      "1",
		Name:         "tesvol1",
		InternalName: "pvc-testvol1",
		Size:         VolumeSizeStr,
		InternalID:   volumeID,
	}

	labels := make(map[string]string)
	labels[drivers.TridentLabelTag] = driver.getTelemetryLabels(ctx)
	labels[storage.ProvisioningLabelTag] = ""

	volume := &api.Volume{
		ID:              volumeID,
		ServiceID:       "dc114541-f3a7-4767-a9a4-cb7e58c60a82",
		Name:            "testvol1",
		Protocol:        api.ProtocolTypeNFS,
		SizeInGigabytes: VolumeSizeI64 / (1 << 30),
		CreationToken:   "pvc-testvol1",
		Status:          api.VolumeStatusAvailable,
	}

	return volConfig, volume
}

func TestResize(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, volume := getStructsForResizeVolume(ctx, driver)
	newSize := uint64(VolumeSizeI64 * 2)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().ResizeVolume(ctx, volume, int64(newSize/(1<<30))).Return(nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusExtendingError, api.VolumeStatusError}, driver.defaultTimeout()).
		Return(api.VolumeStatusAvailable, nil).Times(1)

	result := driver.Resize(ctx, volConfig, newSize)

	assert.Nil(t, result, "not nil")
	assert.Equal(t, strconv.FormatUint(newSize, 10), volConfig.Size, "size mismatch")
}

func TestResize_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, _ := getStructsForResizeVolume(ctx, driver)
	newSize := uint64(VolumeSizeI64 * 2)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result := driver.Resize(ctx, volConfig, newSize)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, VolumeSizeStr, volConfig.Size, "size mismatch")
}

func TestResize_NonExistentVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, _ := getStructsForResizeVolume(ctx, driver)
	newSize := uint64(VolumeSizeI64 * 2)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(nil, errors.NotFoundError("not found")).Times(1)

	result := driver.Resize(ctx, volConfig, newSize)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, VolumeSizeStr, volConfig.Size)
}

func TestResize_VolumeNotAvailable(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, volume := getStructsForResizeVolume(ctx, driver)
	volume.Status = api.VolumeStatusError
	newSize := uint64(VolumeSizeI64 * 2)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)

	result := driver.Resize(ctx, volConfig, newSize)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, VolumeSizeStr, volConfig.Size, "size mismatch")
}

func TestResize_NoSizeChange(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, volume := getStructsForResizeVolume(ctx, driver)
	newSize := uint64(VolumeSizeI64)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)

	result := driver.Resize(ctx, volConfig, newSize)

	assert.Nil(t, result, "not nil")
	assert.Equal(t, VolumeSizeStr, volConfig.Size, "size mismatch")
}

func TestResize_ShrinkingVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, volume := getStructsForResizeVolume(ctx, driver)
	newSize := uint64(VolumeSizeI64 / 2)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)

	result := driver.Resize(ctx, volConfig, newSize)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, VolumeSizeStr, volConfig.Size, "size mismatch")
}

func TestResize_AboveMaximumSize(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.LimitVolumeSize = strconv.FormatInt(VolumeSizeI64+1, 10)

	volConfig, volume := getStructsForResizeVolume(ctx, driver)
	driver.Config.LimitVolumeSize = strconv.FormatInt(VolumeSizeI64+1, 10)
	newSize := uint64(VolumeSizeI64 * 2)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)

	result := driver.Resize(ctx, volConfig, newSize)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, VolumeSizeStr, volConfig.Size, "size mismatch")
}

func TestResize_VolumeResizeFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, volume := getStructsForResizeVolume(ctx, driver)
	newSize := uint64(VolumeSizeI64 * 2)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().ResizeVolume(ctx, volume, int64(newSize/(1<<30))).Return(errFailed).Times(1)

	result := driver.Resize(ctx, volConfig, newSize)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, VolumeSizeStr, volConfig.Size, "size mismatch")
}

func TestResize_WaitForVolumeStatusFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, volume := getStructsForResizeVolume(ctx, driver)
	newSize := uint64(VolumeSizeI64 * 2)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().ResizeVolume(ctx, volume, int64(newSize/(1<<30))).Return(nil).Times(1)
	mockAPI.EXPECT().WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusExtendingError, api.VolumeStatusError}, driver.defaultTimeout()).
		Return("", errFailed).Times(1)

	result := driver.Resize(ctx, volConfig, newSize)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, VolumeSizeStr, volConfig.Size, "size should be unchanged when wait fails")
}

func TestGetStorageBackendSpecs(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	driver.populateConfigurationDefaults(ctx, &driver.Config)
	driver.initializeStoragePools(ctx)
	driver.initializeTelemetry(ctx, BackendUUID)

	backend := storage.NewTestStorageBackend()
	backend.ClearStoragePools()

	result := driver.GetStorageBackendSpecs(ctx, backend)

	assert.Nil(t, result, "not nil")
	assert.Equal(t, "ovhefs_1-cli", backend.Name(), "backend name mismatch")
	for _, pool := range driver.pools {
		assert.Equal(t, backend, pool.Backend(), "pool-backend mismatch")
		p, ok := backend.StoragePools().Load("ovhefs_1-cli_pool")
		assert.True(t, ok)
		assert.Equal(t, pool, p.(storage.Pool), "backend-pool mismatch")
	}
}

func TestGetStorageBackendPools(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	pools := []*api.CapacityPool{
		{
			ID:           "d2ff2486-a82e-42b8-be8c-36618635cd41",
			Name:         "d2ff2486-a82e-42b8-be8c-36618635cd41",
			Region:       "eu-west-rbx",
			ServiceLevel: api.PerformanceLevelPremium,
			Status:       "running",
		},
		{
			ID:           "4c7c22e6-394d-4c49-a3b9-61710dcd3463",
			Name:         "4c7c22e6-394d-4c49-a3b9-61710dcd3463",
			Region:       "eu-west-rbx",
			ServiceLevel: api.PerformanceLevelPremium,
			Status:       "running",
		},
	}

	mockAPI.EXPECT().CapacityPoolsForStoragePools(ctx).Return(pools)

	backendPools := driver.getStorageBackendPools(ctx)
	t.Log(backendPools)
	assert.Equal(t, len(pools), len(backendPools))

	expectedPool1, actualPool1 := pools[0], backendPools[0]
	assert.NotNil(t, expectedPool1)
	assert.NotNil(t, actualPool1)
	assert.Equal(t, expectedPool1.ID, actualPool1.CapacityPool)

	expectedPool2, actualPool2 := pools[1], backendPools[1]
	assert.NotNil(t, expectedPool2)
	assert.Equal(t, expectedPool2.ID, actualPool2.CapacityPool)
}

func TestCreatePrepare(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	tridentconfig.UsingPassthroughStore = true
	storagePrefix := "myPrefix-"
	driver.Config.StoragePrefix = &storagePrefix

	storagePool := driver.pools["efs_pool"]
	volConfig := &storage.VolumeConfig{Name: "testvol1"}

	driver.CreatePrepare(ctx, volConfig, storagePool)

	assert.Equal(t, "myPrefix-testvol1", volConfig.InternalName)
}

func TestGetStorageBackendPhysicalPoolNames(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	result := driver.GetStorageBackendPhysicalPoolNames(ctx)

	assert.Equal(t, []string{}, result, "physical pool names mismatch")
}

func TestInternalVolumeName_PassthroughStorage(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	tridentconfig.UsingPassthroughStore = true
	storagePrefix := "myPrefix-"
	driver.Config.StoragePrefix = &storagePrefix

	storagePool := driver.pools["efs_pool"]
	volConfig := &storage.VolumeConfig{Name: "testvol1"}

	result := driver.GetInternalVolumeName(ctx, volConfig, storagePool)

	assert.Equal(t, "myPrefix-testvol1", result, "internal name mismatch")
}

func TestGetInternalVolumeName_CSI(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	tridentconfig.UsingPassthroughStore = false
	storagePrefix := "myPrefix-"
	driver.Config.StoragePrefix = &storagePrefix

	storagePool := driver.pools["efs_pool"]
	volConfig := &storage.VolumeConfig{Name: "pvc-463d73cc-0cb1-4c14-b14c-ed96d31a342b"}

	result := driver.GetInternalVolumeName(ctx, volConfig, storagePool)

	assert.Equal(t, "pvc-463d73cc-0cb1-4c14-b14c-ed96d31a342b", result, "internal name mismatch")
}

func TestGetInternalVolumeName_NonCSI(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	tridentconfig.UsingPassthroughStore = false
	storagePrefix := "myPrefix-"
	driver.Config.StoragePrefix = &storagePrefix

	storagePool := driver.pools["efs_pool"]
	volConfig := &storage.VolumeConfig{Name: "testvol1"}

	result := driver.GetInternalVolumeName(ctx, volConfig, storagePool)

	efsRegex := regexp.MustCompile(`^efs-[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	assert.True(t, efsRegex.MatchString(result), "internal name mismatch")
}

func TestCreateFollowup_NFSVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.NASType = sa.NFS

	volConfig, volume, accessPaths, _ := getStructsForPublishNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().VolumeAccessPaths(ctx, volume).Return(accessPaths, nil).Times(1)

	result := driver.CreateFollowup(ctx, volConfig)

	assert.Nil(t, result, "not nil")
	assert.Equal(t, strings.Split(accessPaths[0].Path, ":")[0], volConfig.AccessInfo.NfsServerIP, "NFS server IP mismatch")
	assert.Equal(t, "/"+volume.CreationToken, volConfig.AccessInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, sa.NFS, volConfig.FileSystem, "filesystem type mismatch")
}

func TestCreateFollowup_SMBVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)
	driver.Config.NASType = sa.SMB

	volConfig, volume, accessPaths, _ := getStructsForPublishNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().VolumeAccessPaths(ctx, volume).Return(accessPaths, nil).Times(1)

	result := driver.CreateFollowup(ctx, volConfig)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, "", volConfig.AccessInfo.NfsServerIP, "NFS server IP mismatch")
	assert.NotEqual(t, "", volConfig.AccessInfo.NfsPath, "NFS path is empty")
	assert.Equal(t, "/"+volConfig.InternalName, volConfig.AccessInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "", volConfig.FileSystem, "filesystem type mismatch")
}

func TestCreateFollowup_ROClone_NFSVolume(t *testing.T) {
	// TODO: (feat) RO clone support

	/*
		mockAPI, driver := newMockEFSDriver(t)
		driver.initializeTelemetry(ctx, BackendUUID)
		driver.Config.NASType = "nfs"

		volConfig, volume, accesPaths, _ := getStructsForPublishNFSVolume(ctx, driver)
		volConfig.CloneSourceVolumeInternal = volConfig.Name
		volConfig.CloneSourceSnapshot = SnapshotID
		volConfig.ReadOnlyClone = true

		mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
		mockAPI.EXPECT().GetVolumeByCreationToken(ctx, volConfig.CloneSourceVolumeInternal).Return(volume, nil).Times(1)
		mockAPI.EXPECT().GetVolumeAccessPaths(ctx, volume).Return(accesPaths, nil).Times(1)

		result := driver.CreateFollowup(ctx, volConfig)

		assert.Nil(t, result, "not nil")
		assert.NoError(t, result, "error occured")
		assert.Equal(t, strings.Split(accesPaths[0].Path, ":")[0], volConfig.AccessInfo.NfsServerIP, "NFS server IP mismatch")
		assert.Equal(t, "/testvol1/.snapshot/987b71e8-1e08-448c-b5a4-6f6d5b9a7d8a", volConfig.AccessInfo.NfsPath, "NFS path mismatch")
		assert.Equal(t, "nfs", volConfig.FileSystem, "filesystem type mismatch")
	*/
}

func TestCreateFollowup_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, _, _, _ := getStructsForPublishNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result := driver.CreateFollowup(ctx, volConfig)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, "", volConfig.AccessInfo.NfsServerIP, "NFS server IP mismatch")
	assert.NotEqual(t, "", volConfig.AccessInfo.NfsPath, "NFS path is empty")
	assert.Equal(t, "/"+volConfig.InternalName, volConfig.AccessInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "", volConfig.FileSystem, "filesystem type mismatch")
}

func TestCreateFollowup_NonexistentVolume(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, _, _, _ := getStructsForPublishNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(nil, errors.NotFoundError("not ound error")).Times(1)

	result := driver.CreateFollowup(ctx, volConfig)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, "", volConfig.AccessInfo.NfsServerIP, "NFS server IP mismatch")
	assert.NotEqual(t, "", volConfig.AccessInfo.NfsPath, "NFS path is empty")
	assert.Equal(t, "/"+volConfig.InternalName, volConfig.AccessInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "", volConfig.FileSystem, "filesystem type mismatch")
}

func TestCreateFollowup_ROClone_NonexistentVolume(t *testing.T) {
	// TODO: (feat) RO clone support

	/*
		mockAPI, driver := newMockEFSDriver(t)
		driver.initializeTelemetry(ctx, BackendUUID)

		volConfig, _, _, _ := getStructsForPublishNFSVolume(ctx, driver)
		volConfig.CloneSourceVolumeInternal = volConfig.Name
		volConfig.ReadOnlyClone = true

		mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
		mockAPI.EXPECT().VolumeByCreationToken(ctx, volConfig.CloneSourceVolumeInternal).Return(nil,
			errors.NotFoundError("not found")).Times(1)

		result := driver.CreateFollowup(ctx, volConfig)

		assert.NotNil(t, result, "received nil")
		assert.Error(t, result, "expected error")
		assert.Equal(t, "", volConfig.AccessInfo.NfsServerIP, "NFS server IP mismatch")
		assert.NotEqual(t, "", volConfig.AccessInfo.NfsPath, "NFS path is empty")
		assert.Equal(t, "/"+volConfig.InternalName, volConfig.AccessInfo.NfsPath, "NFS path mismatch")
		assert.Equal(t, "", volConfig.FileSystem, "filesystem type mismatch")
	*/
}

func TestCreateFollowup_VolumeNotAvailable(t *testing.T) {
	nonAvailableStates := []string{
		api.VolumeStatusCreating, api.VolumeStatusDeleting, api.VolumeStatusDeleted,
		api.VolumeStatusExtending, api.VolumeStatusExtendingError, api.VolumeStatusReverting,
		api.VolumeStatusRevertingError, api.VolumeStatusShrinking, api.VolumeStatusShrinkingError,
	}

	for _, state := range nonAvailableStates {
		mockAPI, driver := newMockEFSDriver(t)
		driver.initializeTelemetry(ctx, BackendUUID)

		volConfig, volume, _, _ := getStructsForPublishNFSVolume(ctx, driver)
		volume.Status = state

		mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
		mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)

		result := driver.CreateFollowup(ctx, volConfig)

		assert.NotNil(t, result, "expected error")
		assert.Equal(t, "", volConfig.AccessInfo.NfsServerIP, "NFS server IP mismatch")
		assert.NotEqual(t, "", volConfig.AccessInfo.NfsPath, "NFS path is empty")
		assert.Equal(t, "/"+volConfig.InternalName, volConfig.AccessInfo.NfsPath, "NFS path mismatch")
		assert.Equal(t, "", volConfig.FileSystem, "filesystem type mismatch")
	}
}

func TestCreateFollowup_GetMountTargetsFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, volume, _, _ := getStructsForPublishNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().VolumeAccessPaths(ctx, volume).Return(nil, errFailed).Times(1)

	result := driver.CreateFollowup(ctx, volConfig)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, "", volConfig.AccessInfo.NfsServerIP, "NFS server IP mismatch")
	assert.NotEqual(t, "", volConfig.AccessInfo.NfsPath, "NFS path is empty")
	assert.Equal(t, "/"+volConfig.InternalName, volConfig.AccessInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "", volConfig.FileSystem, "filesystem type mismatch")
}

func TestCreateFollowup_NoMountTargets(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)
	driver.initializeTelemetry(ctx, BackendUUID)

	volConfig, volume, _, _ := getStructsForPublishNFSVolume(ctx, driver)

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volume(ctx, volConfig).Return(volume, nil).Times(1)
	mockAPI.EXPECT().VolumeAccessPaths(ctx, volume).Return(nil, nil).Times(1)

	result := driver.CreateFollowup(ctx, volConfig)

	assert.NotNil(t, result, "expected error")
	assert.Equal(t, "", volConfig.AccessInfo.NfsServerIP, "NFS server IP mismatch")
	assert.NotEqual(t, "", volConfig.AccessInfo.NfsPath, "NFS path is empty")
	assert.Equal(t, "/"+volConfig.InternalName, volConfig.AccessInfo.NfsPath, "NFS path mismatch")
	assert.Equal(t, "", volConfig.FileSystem, "filesystem type mismatch")
}

func TestGetProtocol(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	result := driver.GetProtocol(ctx)

	assert.Equal(t, tridentconfig.File, result)
}

func TestStoreConfig(t *testing.T) {
	commonConfig := &drivers.CommonStorageDriverConfig{
		Version:           1,
		StorageDriverName: "ovh-efs",
		BackendName:       "myOVHEFSBackend",
		DriverContext:     tridentconfig.ContextCSI,
		DebugTraceFlags:   debugTraceFlags,
	}

	_, driver := newMockEFSDriver(t)
	driver.Config.CommonStorageDriverConfig = commonConfig

	persistentConfig := &storage.PersistentStorageBackendConfig{}

	driver.StoreConfig(ctx, persistentConfig)

	assert.Equal(t, json.RawMessage("{}"), driver.Config.CommonStorageDriverConfig.StoragePrefixRaw,
		"raw prefix mismatch")
	assert.Equal(t, driver.Config, *persistentConfig.OVHConfig, "ovh config mismatch")
}

func TestGetVolumeForImport(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	storagePrefix := "myPrefix-"
	driver.Config.StoragePrefix = &storagePrefix

	volume := &api.Volume{
		Name:          "testvol1",
		CreationToken: "myPrefix-testvol1",
		Status:        api.VolumeStatusAvailable,
	}

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, "testvol1").Return(volume, nil).Times(1)

	result, err := driver.GetVolumeForImport(ctx, "testvol1")

	assert.Nil(t, err, "not nil")
	assert.IsType(t, &storage.VolumeExternal{}, result, "type mismatch")
	assert.Equal(t, "1", result.Config.Version)
	assert.Equal(t, "testvol1", result.Config.Name)
	assert.Equal(t, "myPrefix-testvol1", result.Config.InternalName)
}

func TestGetVolumeForImport_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	storagePrefix := "myPrefix-"
	driver.Config.StoragePrefix = &storagePrefix

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	result, err := driver.GetVolumeForImport(ctx, "testvol1")

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "error expected")
}

func TestGetVolumeForImport_GetFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	storagePrefix := "myPrefix-"
	driver.Config.StoragePrefix = &storagePrefix

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().VolumeByMountPointName(ctx, "testvol1").Return(nil, errFailed).Times(1)

	result, err := driver.GetVolumeForImport(ctx, "testvol1")

	assert.Nil(t, result, "not nil")
	assert.NotNil(t, err, "error expected")
}

func TestGetVolumeExternalWrappers(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	storagePrefix := "myPrefix-"
	driver.Config.StoragePrefix = &storagePrefix

	vols := getVolumesForList()
	channel := make(chan *storage.VolumeExternalWrapper, len(vols))

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volumes(ctx).Return(vols, nil).Times(1)

	driver.GetVolumeExternalWrappers(ctx, channel)

	// Read the volumes from the channel
	volumes := make([]*storage.VolumeExternal, 0)
	for wrapper := range channel {
		if wrapper.Error != nil {
			t.FailNow()
		} else {
			volumes = append(volumes, wrapper.Volume)
		}
	}

	assert.Len(t, volumes, 2, "wrong number of volumes")
}

func TestGetVolumeExternalWrappers_DiscoveryFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	storagePrefix := "myPrefix-"
	driver.Config.StoragePrefix = &storagePrefix

	vols := getVolumesForList()
	channel := make(chan *storage.VolumeExternalWrapper, len(vols))

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(errFailed).Times(1)

	driver.GetVolumeExternalWrappers(ctx, channel)

	// Read the volumes from the channel
	var result error
	for wrapper := range channel {
		if wrapper.Error != nil {
			result = wrapper.Error
			break
		}
	}

	assert.NotNil(t, result, "expected error")
}

func TestGetVolumeExternalWrappers_ListFailed(t *testing.T) {
	mockAPI, driver := newMockEFSDriver(t)

	storagePrefix := "myPrefix-"
	driver.Config.StoragePrefix = &storagePrefix

	vols := getVolumesForList()
	channel := make(chan *storage.VolumeExternalWrapper, len(vols))

	mockAPI.EXPECT().RefreshOVHResources(ctx).Return(nil).Times(1)
	mockAPI.EXPECT().Volumes(ctx).Return(nil, errFailed).Times(1)

	driver.GetVolumeExternalWrappers(ctx, channel)

	// Read the volumes from the channel
	var result error
	for wrapper := range channel {
		if wrapper.Error != nil {
			result = wrapper.Error
			break
		}
	}

	assert.NotNil(t, result, "expected error")
}

func TestGetUpdateType_NoFlaggedChanges(t *testing.T) {
	_, oldDriver := newMockEFSDriver(t)
	oldDriver.volumeCreateTimeout = 1 * time.Second

	_, newDriver := newMockEFSDriver(t)
	newDriver.volumeCreateTimeout = 2 * time.Second

	result := newDriver.GetUpdateType(ctx, oldDriver)

	expectedBitmap := &roaring.Bitmap{}

	assert.Equal(t, expectedBitmap, result, "bitmap mismatch")
}

func TestGetUpdateType_WrongDriverType(t *testing.T) {
	oldDriver := &fake.StorageDriver{
		Config:             drivers.FakeStorageDriverConfig{},
		Volumes:            make(map[string]storagefake.Volume),
		DestroyedVolumes:   make(map[string]bool),
		Snapshots:          make(map[string]map[string]*storage.Snapshot),
		DestroyedSnapshots: make(map[string]bool),
		Secret:             "secret",
	}

	_, newDriver := newMockEFSDriver(t)
	newDriver.volumeCreateTimeout = 2 * time.Second

	result := newDriver.GetUpdateType(ctx, oldDriver)

	expectedBitmap := &roaring.Bitmap{}
	expectedBitmap.Add(storage.InvalidUpdate)

	assert.Equal(t, expectedBitmap, result, "bitmap mismatch")
}

func TestGetUpdateType_OtherChanges(t *testing.T) {
	_, oldDriver := newMockEFSDriver(t)
	prefix1 := "prefix1-"
	oldDriver.Config.StoragePrefix = &prefix1
	oldDriver.Config.Credentials = map[string]string{
		drivers.KeyName: "secret1",
		drivers.KeyType: string(drivers.CredentialStoreK8sSecret),
	}

	_, newDriver := newMockEFSDriver(t)
	prefix2 := "prefix2-"
	newDriver.Config.StoragePrefix = &prefix2
	newDriver.Config.Credentials = map[string]string{
		drivers.KeyName: "secret2",
		drivers.KeyType: string(drivers.CredentialStoreK8sSecret),
	}

	result := newDriver.GetUpdateType(ctx, oldDriver)

	expectedBitmap := &roaring.Bitmap{}
	expectedBitmap.Add(storage.PrefixChange)
	expectedBitmap.Add(storage.CredentialsChange)

	assert.Equal(t, expectedBitmap, result, "bitmap mismatch")
}

func TestReconcileNodeAccess(t *testing.T) {
	_, driver := newMockEFSDriver(t)

	result := driver.ReconcileNodeAccess(ctx, nil, "", "")

	assert.Nil(t, result, "not nil")
}

func TestValidateStoragePrefix(t *testing.T) {
	tests := []struct {
		Name          string
		StoragePrefix string
		Valid         bool
	}{
		// Invalid storage prefixes
		{
			Name:          "storage prefix starts with plus",
			StoragePrefix: "+abcd-ABC",
		},
		{
			Name:          "storage prefix starts with digit",
			StoragePrefix: "1abcd-ABC",
		},
		{
			Name:          "storage prefix starts with underscore",
			StoragePrefix: "_abcd-ABC",
		},
		{
			Name:          "storage prefix contains digits",
			StoragePrefix: "abcd-123-ABC",
		},
		{
			Name:          "storage prefix contains underscore",
			StoragePrefix: "ABCD_abc",
		},
		{
			Name:          "storage prefix has plus",
			StoragePrefix: "abcd+ABC",
		},
		{
			Name:          "storage prefix is single digit",
			StoragePrefix: "1",
		},
		{
			Name:          "storage prefix is single underscore",
			StoragePrefix: "_",
		},
		{
			Name:          "storage prefix is single colon",
			StoragePrefix: ":",
		},
		{
			Name:          "storage prefix is single dash",
			StoragePrefix: "-",
		},
		// Valid storage prefixes
		{
			Name:          "storage prefix is single letter",
			StoragePrefix: "a",
			Valid:         true,
		},
		{
			Name:          "storage prefix has only letters and dash",
			StoragePrefix: "abcd-efgh",
			Valid:         true,
		},
		{
			Name:          "storage prefix ends with dash",
			StoragePrefix: "abcd-efgh-",
			Valid:         true,
		},
		{
			Name:          "storage prefix has capital letters",
			StoragePrefix: "ABCD",
			Valid:         true,
		},
		{
			Name:          "storage prefix has letters and capital letters",
			StoragePrefix: "abcd-EFGH",
			Valid:         true,
		},
		{
			Name:          "storage prefix is empty",
			StoragePrefix: "",
			Valid:         true,
		},
	}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			err := validateStoragePrefix(test.StoragePrefix)
			if test.Valid {
				assert.NoError(t, err, "should be valid")
			} else {
				assert.Error(t, err, "should be invalid")
			}
		})
	}
}

func TestGetCommonConfig(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	mockAPI := mockapi.NewMockOVHClient(mockCtrl)

	driver := *newTestEFSDriver(mockAPI)
	driver.Config.BackendName = "efs"
	driver.Config.ServiceLevel = api.PerformanceLevelPremium

	result := driver.GetCommonConfig(ctx)

	assert.Equal(t, driver.Config.CommonStorageDriverConfig, result, "common config mismatch")
}
