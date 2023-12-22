package ovh

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/google/uuid"
	"go.uber.org/multierr"
	"golang.org/x/sync/errgroup"

	tridentconfig "github.com/netapp/trident/config"
	. "github.com/netapp/trident/logging"
	"github.com/netapp/trident/pkg/capacity"
	"github.com/netapp/trident/pkg/convert"
	"github.com/netapp/trident/storage"
	sa "github.com/netapp/trident/storage_attribute"

	sc "github.com/netapp/trident/storage_class"
	drivers "github.com/netapp/trident/storage_drivers"
	"github.com/netapp/trident/storage_drivers/ovh/api"
	"github.com/netapp/trident/utils/errors"
	"github.com/netapp/trident/utils/models"
	"github.com/netapp/trident/utils/nfs"
)

const (
	MinimumVolumeSizeBytes       = uint64(1073741824)   // 100 GiB
	MinimumEFSVolumeSizeBytes    = uint64(107374182400) // 100 GiB
	MaximumVolumesPerStoragePool = 50

	defaultNFSMountOptions = "rw,hard,rsize=65536,wsize=65536,nfsvers=3,tcp"
	defaultServiceLevel    = api.PerformanceLevelPremium
	defaultSnapshotDir     = "true"
	defaultLimitVolumeSize = ""
	defaultExportRule      = "0.0.0.0/0"

	// Constants for internal pool attributes

	Size          = "size"
	ServiceLevel  = "serviceLevel"
	SnapshotDir   = "snapshotDir"
	ExportRule    = "exportRule"
	Region        = "region"
	StorageClass  = "storageClass"
	CapacityPools = "capacityPools"

	nfsVersion3 = "3"
)

var (
	supportedNFSVersions = []string{nfsVersion3}

	storagePrefixRegex       = regexp.MustCompile(`^$|^[a-zA-Z][a-zA-Z-]*$`)
	volumeNameRegex          = regexp.MustCompile(`^[a-zA-Z]([a-zA-Z0-9-_]{0,62}[a-zA-Z0-9])?$`)
	volumeCreationTokenRegex = regexp.MustCompile(`^[a-zA-Z]([a-zA-Z0-9-_]{0,62}[a-zA-Z0-9])?$`)
	// csiRegex from ANF driver
	csiRegex = regexp.MustCompile(`^pvc-[\da-fA-F]{8}-[\da-fA-F]{4}-[\da-fA-F]{4}-[\da-fA-F]{4}-[\da-fA-F]{12}$`)
)

// NASStorageDriver is for storage provisioning using the OVH Enterprise File Storage service.
type NASStorageDriver struct {
	initialized         bool
	Config              drivers.OVHNASStorageDriverConfig
	API                 api.OVHClient
	telemetry           *Telemetry
	pools               map[string]storage.Pool
	volumeCreateTimeout time.Duration
}

type Telemetry struct {
	tridentconfig.Telemetry
	Plugin string `json:"plugin"`
}

// Name returns the name of this driver.
func (d *NASStorageDriver) Name() string {
	return tridentconfig.OVHNASStorageDriverName
}

// GetConfig returns the config of the drivers.
func (d *NASStorageDriver) GetConfig() drivers.DriverConfig {
	return &d.Config
}

// defaultBackendName returns the default name of the backend managed by this driver instance.
func (d *NASStorageDriver) defaultBackendName() string {
	return fmt.Sprintf("%s_%s", strings.Replace(d.Name(), "-", "", -1), d.Config.ClientID[0:5])
}

// BackendName returns the name of the backend managed by this driver instance.
func (d *NASStorageDriver) BackendName() string {
	if d.Config.BackendName != "" {
		return d.Config.BackendName
	} else {
		// Use the old naming scheme if no backend is specified
		return d.defaultBackendName()
	}
}

// poolName constructs the name of the pool reported by this driver instance
func (d *NASStorageDriver) poolName(name string) string {
	return fmt.Sprintf("%s_%s", d.BackendName(), strings.Replace(name, "-", "", -1))
}

// validateName checks that the name of a new volume matches the requirements of a creation token
func (d *NASStorageDriver) validateVolumeName(name string) error {
	if !volumeNameRegex.MatchString(name) {
		return fmt.Errorf("volume name '%s' is not allowed; it must be 1-63 characters long, "+
			"begin with a letter, and contain only letters, digits, and hyphens", name)
	}
	return nil
}

func (d *NASStorageDriver) validateCreationToken(name string) error {
	if !volumeCreationTokenRegex.MatchString(name) {
		return fmt.Errorf("volume internal name '%s' is not allowed; it be 1-255 characters long, "+
			"begin with a letter, and contain only letters, digits, hyphes and underscores", name)
	}
	return nil
}

// defaultCreateTimeout sets the driver timeout for volume create/delete operations. Docker gets more time, since
// it doesn't have a mechanism to retry.
func (d *NASStorageDriver) defaultCreateTimeout() time.Duration {
	switch d.Config.DriverContext {
	case tridentconfig.ContextDocker:
		return tridentconfig.DockerCreateTimeout
	default:
		return api.VolumeCreateTimeout
	}
}

// defaultTimeout controls the driver timeout for most workflows.
func (d *NASStorageDriver) defaultTimeout() time.Duration {
	switch d.Config.DriverContext {
	case tridentconfig.ContextDocker:
		return tridentconfig.DockerDefaultTimeout
	default:
		return api.DefaultTimeout
	}
}

// Initialize initializes this driver from the provided config.
func (d *NASStorageDriver) Initialize(
	ctx context.Context, context tridentconfig.DriverContext, configJSON string,
	commonConfig *drivers.CommonStorageDriverConfig, backendSecret map[string]string, backendUUID string,
) error {
	fields := LogFields{"Method": "Initialize", "Type": "NASStorageDriver"}
	Logd(ctx, commonConfig.StorageDriverName, commonConfig.DebugTraceFlags["method"]).WithFields(fields).
		Trace(">>>> Initialize")
	defer Logd(ctx, commonConfig.StorageDriverName, commonConfig.DebugTraceFlags["method"]).WithFields(fields).
		Trace("<<<< Initialize")

	commonConfig.DriverContext = context

	// Initialize the driver's CommonStorageDriverConfig
	d.Config.CommonStorageDriverConfig = commonConfig

	// Parse the config
	config, err := d.initializeOVHConfig(ctx, configJSON, commonConfig, backendSecret)
	if err != nil {
		return fmt.Errorf("error initializing %s driver. %v", d.Name(), err)
	}
	d.Config = *config

	if err = d.populateConfigurationDefaults(ctx, &d.Config); err != nil {
		return fmt.Errorf("could not populate configuration defaults: %v", err)
	}

	d.initializeStoragePools(ctx)
	d.initializeTelemetry(ctx, backendUUID)

	if err = d.initializeOVHAPIClient(ctx, &d.Config); err != nil {
		return fmt.Errorf("error initializing %s OVH API client. %v", d.Name(), err)
	}

	if err = d.validate(ctx); err != nil {
		return fmt.Errorf("error validating %s driver. %v", d.Name(), err)
	}

	// Identify non-overlapping storage backend pools on the driver backend.
	pools, err := drivers.EncodeStorageBackendPools(ctx, commonConfig, d.getStorageBackendPools(ctx))
	if err != nil {
		return fmt.Errorf("failed to encode storage backend pool: %v", err)
	}
	d.Config.BackendPools = pools

	Logc(ctx).WithFields(LogFields{
		"StoragePrefix":              *config.StoragePrefix,
		"Size":                       config.Size,
		"ServiceLevel":               config.ServiceLevel,
		"NfsMountOptions":            config.NFSMountOptions,
		"LimitVolumeSize":            config.LimitVolumeSize,
		"ExportRule":                 config.ExportRule,
		"VolumeCreateTimeoutSeconds": config.VolumeCreateTimeout,
	}).Info("Initialized driver.")

	d.initialized = true
	return nil
}

// Initialized returns whether this driver has been initialized (and not terminated).
func (d *NASStorageDriver) Initialized() bool {
	return d.initialized
}

// Terminate stops the driver prior to its being unloaded.
func (d *NASStorageDriver) Terminate(ctx context.Context, _ string) {
	fields := LogFields{"Method": "Terminate", "Type": "NASStorageDriver"}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> Terminate")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< Terminate")

	d.initialized = false
}

// populateConfigurationDefaults fills in default values for configuration settings if not supplied in the config file.
func (d *NASStorageDriver) populateConfigurationDefaults(
	ctx context.Context, config *drivers.OVHNASStorageDriverConfig,
) error {
	fields := LogFields{"Method": "populateConfigurationDefaults", "Type": "NFSStorageDriver"}
	Logd(ctx, config.StorageDriverName, config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> populateConfigurationDefaults")
	defer Logd(ctx, config.StorageDriverName, config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< populateConfigurationDefaults")

	if config.StoragePrefix == nil {
		defaultPrefix := drivers.GetDefaultStoragePrefix(config.DriverContext)
		defaultPrefix = strings.Replace(defaultPrefix, "_", "-", -1)
		config.StoragePrefix = &defaultPrefix
	}

	if config.Size == "" {
		config.Size = drivers.DefaultVolumeSize
	}

	if config.ServiceLevel == "" {
		config.ServiceLevel = defaultServiceLevel
	}

	if config.NFSMountOptions == "" {
		config.NFSMountOptions = defaultNFSMountOptions
	}

	// Snapshot dir cannot be configured.
	config.SnapshotDir = defaultSnapshotDir

	if config.LimitVolumeSize == "" {
		config.LimitVolumeSize = defaultLimitVolumeSize
	}

	if config.ExportRule == "" {
		config.ExportRule = defaultExportRule
	}

	// NAS type cannot be configured.
	config.NASType = sa.NFS

	// VolumeCreateTimeoutSeconds is the timeout value in seconds.
	volumeCreateTimeout := d.defaultCreateTimeout()
	if config.VolumeCreateTimeout != "" {
		i, err := strconv.ParseInt(d.Config.VolumeCreateTimeout, 10, 64)
		if err != nil {
			Logc(ctx).WithField("interval", d.Config.VolumeCreateTimeout).Errorf(
				"Invalid volume create timeout period. %v", err)
			return err
		}
		volumeCreateTimeout = time.Duration(i) * time.Second
	}
	d.volumeCreateTimeout = volumeCreateTimeout

	Logc(ctx).WithFields(LogFields{
		"StoragePrefix":              *config.StoragePrefix,
		"Size":                       config.Size,
		"ServiceLevel":               config.ServiceLevel,
		"NFSMountOptions":            config.NFSMountOptions,
		"SnapshotDir":                config.SnapshotDir,
		"LimitVolumeSize":            config.LimitVolumeSize,
		"ExportRule":                 config.ExportRule,
		"VolumeCreateTimeoutSeconds": config.VolumeCreateTimeout,
	}).Debug("Configuration defaults.")

	return nil
}

// initializeStoragePools defines the pools reported to Trident, whether physical or virtual.
func (d *NASStorageDriver) initializeStoragePools(ctx context.Context) {
	d.pools = make(map[string]storage.Pool)

	if len(d.Config.Storage) == 0 {
		Logc(ctx).Debug("No vpools defined, reporting single pool.")

		// No vpools defined, so report region/zone as a single pool
		pool := storage.NewStoragePool(nil, d.poolName("pool"))

		pool.Attributes()[sa.BackendType] = sa.NewStringOffer(d.Name())
		pool.Attributes()[sa.Snapshots] = sa.NewBoolOffer(true)
		pool.Attributes()[sa.Clones] = sa.NewBoolOffer(true)
		pool.Attributes()[sa.Encryption] = sa.NewBoolOffer(false)
		pool.Attributes()[sa.Replication] = sa.NewBoolOffer(false)
		pool.Attributes()[sa.Labels] = sa.NewLabelOffer(d.Config.Labels)
		pool.Attributes()[sa.NASType] = sa.NewStringOffer(d.Config.NASType)

		if d.Config.Region != "" {
			pool.Attributes()[sa.Region] = sa.NewStringOffer(d.Config.Region)
		}
		// TODO: (feat) multi-az support
		/*
			   if d.Config.Zone != "" {
					pool.Attributes()[sa.Zone] = sa.NewStringOffer(d.Config.Zone)
			   }
		*/

		pool.InternalAttributes()[Size] = d.Config.Size
		// TODO: (feat) UNIX permissions
		// pool.InternalAttributes()[UnixPermissions] = d.Config.UnixPermissions
		pool.InternalAttributes()[ServiceLevel] = convert.ToTitle(d.Config.ServiceLevel)
		pool.InternalAttributes()[SnapshotDir] = d.Config.SnapshotDir
		pool.InternalAttributes()[ExportRule] = d.Config.ExportRule
		pool.InternalAttributes()[CapacityPools] = strings.Join(d.Config.CapacityPools, ",")

		pool.SetSupportedTopologies(d.Config.SupportedTopologies)

		d.pools[pool.Name()] = pool
	} else {
		Logc(ctx).Debug("One or more vpools defined.")

		// Report a pool for each virtual pool in the config
		for index, vpool := range d.Config.Storage {

			region := d.Config.Region
			if vpool.Region != "" {
				region = vpool.Region
			}

			// TODO: (feat) multi-az support
			/*
				zone := d.Config.Zone
				if vpool.Zone != "" {
					zone = vpool.Zone
				}
			*/

			size := d.Config.Size
			if vpool.Size != "" {
				size = vpool.Size
			}

			supportedTopologies := d.Config.SupportedTopologies
			if vpool.SupportedTopologies != nil {
				supportedTopologies = vpool.SupportedTopologies
			}

			capacityPools := d.Config.CapacityPools
			if vpool.CapacityPools != nil {
				capacityPools = vpool.CapacityPools
			}

			serviceLevel := d.Config.ServiceLevel
			if vpool.ServiceLevel != "" {
				serviceLevel = vpool.ServiceLevel
			}

			snapshotDir := d.Config.SnapshotDir
			// TODO: (feat) snapshot dir access
			/*
				if vpool.SnapshotDir != "" {
					snapDirFormatted, err := convert.ToFormattedBool(vpool.SnapshotDir)
					if err != nil {
						Logc(ctx).WithError(err).Errorf("Invalid boolean value for vpool's snapshotDir: %v.",
							vpool.SnapshotDir)
					}
					snapshotDir = snapDirFormatted
				}
			*/

			exportRule := d.Config.ExportRule
			if vpool.ExportRule != "" {
				exportRule = vpool.ExportRule
			}

			pool := storage.NewStoragePool(nil, d.poolName(fmt.Sprintf("pool_%d", index)))

			pool.Attributes()[sa.BackendType] = sa.NewStringOffer(d.Name())
			pool.Attributes()[sa.Snapshots] = sa.NewBoolOffer(true)
			pool.Attributes()[sa.Clones] = sa.NewBoolOffer(true)
			pool.Attributes()[sa.Encryption] = sa.NewBoolOffer(false)
			pool.Attributes()[sa.Replication] = sa.NewBoolOffer(false)
			pool.Attributes()[sa.Labels] = sa.NewLabelOffer(d.Config.Labels, vpool.Labels)

			nasType := d.Config.NASType
			if vpool.NASType != "" {
				nasType = vpool.NASType
			}

			pool.Attributes()[sa.NASType] = sa.NewStringOffer(nasType)

			if region != "" {
				pool.Attributes()[sa.Region] = sa.NewStringOffer(region)
			}

			// TODO: (feat) multi-az support
			/*
				if zone != "" {
					pool.Attributes()[sa.Zone] = sa.NewStringOffer(zone)
				}
			*/

			pool.InternalAttributes()[Size] = size
			// TODO: (feat) UNIX permissions
			// pool.InternalAttributes()[UnixPermissions] = unixPermissions
			pool.InternalAttributes()[ServiceLevel] = convert.ToTitle(serviceLevel)
			pool.InternalAttributes()[SnapshotDir] = snapshotDir
			pool.InternalAttributes()[ExportRule] = exportRule
			pool.InternalAttributes()[CapacityPools] = strings.Join(capacityPools, ",")

			pool.SetSupportedTopologies(supportedTopologies)

			d.pools[pool.Name()] = pool
		}

	}

	return
}

// initializeTelemetry assembles all the telemetry data to be used as volume labels.
func (d *NASStorageDriver) initializeTelemetry(_ context.Context, backendUUID string) {
	telemetry := tridentconfig.OrchestratorTelemetry
	telemetry.TridentBackendUUID = backendUUID
	d.telemetry = &Telemetry{
		Telemetry: telemetry,
		Plugin:    d.Name(),
	}
}

// initializeOVHConfig parses the OVH config, mixing in the specified common config.
func (d *NASStorageDriver) initializeOVHConfig(
	ctx context.Context, configJSON string, commonConfig *drivers.CommonStorageDriverConfig,
	backendSecret map[string]string,
) (*drivers.OVHNASStorageDriverConfig, error) {
	fields := LogFields{"Method": "initializeOVHConfig", "Type": "NASStorageDriver"}
	Logd(ctx, commonConfig.StorageDriverName, commonConfig.DebugTraceFlags["method"]).WithFields(fields).
		Trace(">>>> initializeOVHConfig")
	defer Logd(ctx, commonConfig.StorageDriverName, commonConfig.DebugTraceFlags["method"]).WithFields(fields).
		Trace("<<<< initializeOVHConfig")

	config := &drivers.OVHNASStorageDriverConfig{}
	config.CommonStorageDriverConfig = commonConfig

	// decode configJSON into OVHNASStorageDriverConfig object
	err := json.Unmarshal([]byte(configJSON), &config)
	if err != nil {
		return nil, fmt.Errorf("could not decode JSON configuration. %v", err)
	}

	// Inject secret if not empty
	if len(backendSecret) != 0 {
		err = config.InjectSecrets(backendSecret)
		if err != nil {
			return nil, fmt.Errorf("could not inject backend secret; err: %v", err)
		}
	}

	return config, nil
}

// initializeOVHAPIClient returns an OVH API client.
func (d *NASStorageDriver) initializeOVHAPIClient(
	ctx context.Context, config *drivers.OVHNASStorageDriverConfig,
) error {
	fields := LogFields{"Method": "initializeOVHAPIClient", "Type": "NFSStorageDriver"}
	Logd(ctx, config.StorageDriverName, config.DebugTraceFlags["method"]).WithFields(fields).
		Trace(">>>> initializeOVHAPIClient")
	defer Logd(ctx, config.StorageDriverName, config.DebugTraceFlags["method"]).WithFields(fields).
		Trace("<<<< initializeOVHAPIClient")

	apiTimeout := api.DefaultTimeout
	if config.APITimeout != "" {
		if i, parseErr := strconv.ParseInt(d.Config.APITimeout, 10, 64); parseErr != nil {
			Logc(ctx).WithField("interval", d.Config.APITimeout).WithError(parseErr).
				Error("Invalid value for API timeout.")
			return parseErr
		} else {
			apiTimeout = time.Duration(i) * time.Second
		}
	}

	maxCacheAge := api.DefaultMaxCacheAge
	if config.MaxCacheAge != "" {
		if i, parseErr := strconv.ParseInt(d.Config.MaxCacheAge, 10, 64); parseErr != nil {
			Logc(ctx).WithField("interval", d.Config.MaxCacheAge).WithError(parseErr).
				Error("Invalid value for max cache age.")
			return parseErr
		} else {
			maxCacheAge = time.Duration(i) * time.Second
		}
	}

	client, err := api.NewDriver(&api.ClientConfig{
		Location: config.Location,

		ClientID:       config.ClientID,
		ClientSecret:   config.ClientSecret,
		ClientLocation: config.ClientLocation,

		DebugTraceFlags: config.DebugTraceFlags,
		MaxCacheAge:     maxCacheAge,
		APITimeout:      apiTimeout,
	})
	if err != nil {
		return err
	}

	// Unit tests mock the API layer, so we only use the real API interface if it doesn't already exist.
	if d.API == nil {
		d.API = client
	}

	// The storage pools should already be set up by this point. We register the pools with the
	// API layer to enable matching of storage pools with discovered EFS resources.
	return d.API.Init(ctx, d.pools)
}

// validate ensures the driver configuration and execution environment are valid and working.
func (d *NASStorageDriver) validate(ctx context.Context) error {
	fields := LogFields{"Method": "validate", "Type": "NAStorageDriver"}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> validate")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< validate")

	// Ensure storage prefix is compatible with cloud service
	if err := validateStoragePrefix(*d.Config.StoragePrefix); err != nil {
		return err
	}

	// Validate API client
	if _, err := d.API.Volumes(ctx); err != nil {
		return fmt.Errorf("could not read volumes in %s location with %s client location; %v", d.Config.Location, d.Config.ClientLocation, err)
	}

	Logc(ctx).WithFields(LogFields{
		"location":       d.Config.Location,
		"clientLocation": d.Config.ClientLocation,
	}).Debug("REST API access OK.")

	// Validate pool-level attributes
	for poolName, pool := range d.pools {
		// Validate service level
		serviceLevel := pool.InternalAttributes()[ServiceLevel]
		switch serviceLevel {
		case api.PerformanceLevelPremium, "":
			break
		default:
			return fmt.Errorf("invalid service level in pool %s: %s", poolName,
				pool.InternalAttributes()[ServiceLevel])
		}

		// Validate export rules
		for _, rule := range strings.Split(pool.InternalAttributes()[ExportRule], ",") {
			ipAddr := net.ParseIP(rule)
			_, netAddr, _ := net.ParseCIDR(rule)
			if ipAddr == nil && netAddr == nil {
				return fmt.Errorf("invalid address/CIDR for exportRule in pool %s: %s", poolName, rule)
			}
		}

		// Validate default size
		if _, err := capacity.ToBytes(pool.InternalAttributes()[Size]); err != nil {
			return fmt.Errorf("invalid value for default volume size in pool %s: %v", poolName, err)
		}
	}

	return nil
}

// Create creates a new volume.
func (d *NASStorageDriver) Create(ctx context.Context, volConfig *storage.VolumeConfig, storagePool storage.Pool,
	volAttributes map[string]sa.Request,
) error {
	name := volConfig.InternalName

	fields := LogFields{
		"Method":      "Create",
		"Type":        "NASStorageDriver",
		"name":        name,
		"attrs":       volAttributes,
		"storagePool": storagePool,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> Create")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< Create")

	// Update resource cache
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return fmt.Errorf("could not update OVH EFS resource cache; %v", err)
	}

	// Make sure we got a valid name
	if err := d.validateVolumeName(name); err != nil {
		return err
	}

	// Make sure we got a valid creation token
	if err := d.validateCreationToken(volConfig.Name); err != nil {
		return err
	}

	// Get the pool since most default values are pool-specific
	if storagePool == nil {
		return errors.New("pool not specified")
	}
	pool, ok := d.pools[storagePool.Name()]
	if !ok {
		return fmt.Errorf("pool %s does not exist", storagePool.Name())
	}

	// If both the volume and export rules already exists, bail out
	volumeExists, extantVolume, err := d.API.VolumeExists(ctx, volConfig)
	if err != nil {
		return fmt.Errorf("error checking for existing volume: %v", err)
	}
	if volumeExists {
		if extantVolume.Status == api.VolumeStatusCreating {
			// This is a retry and the volume still isn't ready, so no need to wait further.
			return errors.VolumeCreatingError(
				fmt.Sprintf("volume status is still %s, not %s", api.VolumeStatusCreating, api.VolumeStatusAvailable))
		}

		Logc(ctx).WithFields(LogFields{
			"name":  name,
			"state": extantVolume.Status,
		}).Warning("Volume already exists.")

		exportRulesExists, exportRules, err := d.API.ExportRulesExists(ctx, extantVolume, pool.InternalAttributes()[ExportRule])
		if err != nil {
			return fmt.Errorf("error checking for existing export rule(s): %v", err)
		}
		if exportRulesExists {
			for _, rule := range exportRules {
				if rule.Status == api.ExportRuleStatusApplying || rule.Status == api.ExportRuleStatusQueuedToApply {
					// This is a retry and the volume export rule(s) stil aren't ready, so no need to wait futher.
					return errors.VolumeCreatingError(
						fmt.Sprintf("volume export rule(s) state is not %s", api.ExportRuleStatusActive))
				}
			}

			Logc(ctx).WithFields(LogFields{
				"name":  name,
				"rules": exportRules,
			}).Warning("Volume export rules already exists.")

			// No specific error is returned, so return a generic volume exists error
			return drivers.NewVolumeExistsError(name)
		}
	}

	// Determine volume size in bytes
	requestedSize, err := capacity.ToBytes(volConfig.Size)
	if err != nil {
		return fmt.Errorf("could not convert volume size %s: %v", volConfig.Size, err)
	}
	sizeBytes, err := strconv.ParseUint(requestedSize, 10, 64)
	if err != nil || sizeBytes > math.MaxInt64 {
		return fmt.Errorf("%v is an invalid volume size: %v", volConfig.Size, err)
	}
	if sizeBytes == 0 {
		defaultSize, _ := capacity.ToBytes(pool.InternalAttributes()[Size])
		sizeBytes, _ = strconv.ParseUint(defaultSize, 10, 64)
	}
	if err := drivers.CheckMinVolumeSize(sizeBytes, MinimumVolumeSizeBytes); err != nil {
		return err
	}

	if sizeBytes < MinimumEFSVolumeSizeBytes {
		Logc(ctx).WithFields(LogFields{
			"sizeBytes": sizeBytes,
		}).Warning("Requested size is too small. Setting volume to the minimum allowable (100 GiB).")

		sizeBytes = MinimumEFSVolumeSizeBytes
	}

	if _, _, err := drivers.CheckVolumeSizeLimits(ctx, sizeBytes, d.Config.CommonStorageDriverConfig); err != nil {
		return err
	}

	// Take service level from volume config first (handles Docker case), then from pool
	serviceLevel := convert.ToTitle(volConfig.ServiceLevel)
	if serviceLevel == "" {
		serviceLevel = pool.InternalAttributes()[ServiceLevel]
	}

	// Determine mount options (volume config wins, followed by backend config)
	mountOptions := d.Config.NFSMountOptions
	if volConfig.MountOptions != "" {
		mountOptions = volConfig.MountOptions
	}

	// TODO: (feat) UNIX permissions

	if d.Config.NASType == sa.SMB {
		return fmt.Errorf("SMB/CIFS protocol is not supported by this driver")
	} else {
		_, err = nfs.GetNFSVersionFromMountOptions(mountOptions, nfsVersion3, supportedNFSVersions)
		if err != nil {
			return err
		}
	}

	// TODO: (feat) snapshot dir access
	// NOTE: snapshot dir access value cannot be changed
	snapshotDir := pool.InternalAttributes()[SnapshotDir]
	snapshotDirBool, err := strconv.ParseBool(snapshotDir)
	if err != nil {
		return fmt.Errorf("invalid value for snapshotDir; %v", err)
	}

	// TODO: (feat) volume labels support

	// NOTE: experimental. EFS does not have mutli-AZ support.
	// The volume will be created inside the region requested by the volume topology
	// if the driver backend supports it.
	// TODO: (feat) multi-az support
	var region, zone string
	if len(pool.SupportedTopologies()) > 0 {
		if topology, topologyErr := sc.GetTopologyForVolume(ctx, volConfig, pool); topologyErr != nil {
			return topologyErr
		} else {
			region, zone = sc.GetRegionZoneForTopology(topology)
			zone, _ = strings.CutPrefix(zone, region+"-")
		}
	}

	// Update config to reflect values used to create the volume
	volConfig.Size = strconv.FormatUint(sizeBytes, 10)
	volConfig.ServiceLevel = serviceLevel
	volConfig.SnapshotDir = strconv.FormatBool(snapshotDirBool)
	volConfig.Zone = zone

	// Find matching capacity pools
	cPools := d.API.CapacityPoolsForStoragePool(ctx, pool, serviceLevel)
	if len(cPools) == 0 {
		return fmt.Errorf("no capacity pools found for storage pool %s", pool.Name())
	}

	createErrors := multierr.Combine()

	// Try each capacity pool until one works
	for _, cPool := range cPools {
		if d.Config.NASType == sa.SMB {
			return fmt.Errorf("SMB/CIFS protocol is not supported by this driver.")
		} else {

			Logc(ctx).WithFields(LogFields{
				"capacityPool":  cPool.Name,
				"creationToken": name,
				"size":          sizeBytes,
				"serviceLevel":  serviceLevel,
				//"zone":          zone,
			}).Debug("Creating volume.")

			// Export rule
			exportRule := pool.InternalAttributes()[ExportRule]

			createRequest := &api.VolumeCreateRequest{
				ServiceID:       cPool.ID,
				Name:            volConfig.Name,
				Protocol:        api.ProtocolTypeNFS,
				SizeInGigabytes: int64(sizeBytes) / (1 << 30),
				MountPointName:  name,
			}

			// Create the volume
			volume, createErr := d.API.CreateVolume(ctx, createRequest)
			if createErr != nil {
				errMessage := fmt.Sprintf("OVH EFS pool %s; error creating volume %s: %v", cPool.Name, name, createErr)
				Logc(ctx).Error(errMessage)
				createErrors = multierr.Combine(createErr, fmt.Errorf("%v", errMessage))
				continue
			}

			// Always save the ID so we can retrieve the volume efficiently later
			volConfig.InternalID = volume.ID

			// Wait for creation to complete so that the mount targets are available
			return d.continueCreateVolume(ctx, volume, exportRule, volConfig)
		}
	}

	return createErrors
}

// CreateClone clones an existing volume. If a snapshot is not specified, one is created.
func (d *NASStorageDriver) CreateClone(
	ctx context.Context, sourceVolConfig, cloneVolConfig *storage.VolumeConfig, storagePool storage.Pool,
) error {
	name := cloneVolConfig.InternalName
	source := cloneVolConfig.CloneSourceVolumeInternal
	snapshot := cloneVolConfig.CloneSourceSnapshotInternal

	fields := LogFields{
		"Method":   "CreateClone",
		"Type":     "NASStorageDriver",
		"name":     name,
		"source":   source,
		"snapshot": snapshot,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> CreateClone")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< CreateClone")

	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return fmt.Errorf("could not update OVH resource cache; %v", err)
	}

	// Ensure new volume doesn't exist, fail if so.
	// Get source volume, fail if nonexistent or if wrong region.
	// If a snapshot is specified, read the snapshot from source, fail if nonexistent.
	// If no snapshot is specified, create one, fail if error.
	// Create volume from snapshot.

	// Make sure we got a valid name
	if err := d.validateVolumeName(cloneVolConfig.Name); err != nil {
		return err
	}

	// Make sure we got a valid creation token
	if err := d.validateCreationToken(name); err != nil {
		return err
	}

	// Get the source volume
	sourceVolume, err := d.API.Volume(ctx, sourceVolConfig)
	if err != nil {
		return fmt.Errorf("could not find source volume; %v", err)
	}

	// If the volume already exists, bail out
	volumeExists, extantVolume, err := d.API.VolumeExistsByMountPointName(ctx, name)
	if err != nil {
		return fmt.Errorf("error checking for existing volume: %v", err)
	}
	if volumeExists {
		if extantVolume.Status == api.VolumesStatusCreatingFromSnapshot {
			// This is a retry and the volume still isn't ready, so no need to wait further.
			return errors.VolumeCreatingError(fmt.Sprintf("volume state is %s, not %s",
				extantVolume.Status, api.VolumeStatusAvailable))
		}

		// Volume is maybe in terminal state. If there's a error, get it.
		if err = d.waitForVolumeCreate(ctx, extantVolume); err != nil {
			return err
		}

		// No specific error is returned, so return a generic volume exists error.
		return drivers.NewVolumeExistsError(name)
	}

	var sourceSnapshot *api.Snapshot

	if snapshot != "" {
		// Get the source snapshot
		if sourceSnapshot, err = d.API.SnapshotForVolume(ctx, sourceVolume, snapshot); err != nil {
			return fmt.Errorf("could not find source snapshot: %v", err)
		}

		// Ensure snapshot is in a usable state
		if sourceSnapshot.Status != api.SnapshotStatusAvailable {
			return fmt.Errorf("source snapshot state is '%s', it must be '%s'",
				sourceSnapshot.Status, api.SnapshotStatusAvailable)
		}

		Logc(ctx).WithFields(LogFields{
			"snapshot": snapshot,
			"source":   sourceVolume.Name,
		}).Debug("Found source snapshot.")

	} else {
		// No source snapshot specified, so create one
		snapName := "snap-" + strings.ToLower(time.Now().UTC().Format(storage.SnapshotNameFormat))

		Logc(ctx).WithFields(LogFields{
			"snapshot": snapName,
			"source":   sourceVolume.Name,
		}).Debug("Creating source snapshot.")

		if sourceSnapshot, err = d.API.CreateSnapshot(ctx, sourceVolume, snapName); err != nil {
			return fmt.Errorf("could not create source snapshot: %v", err)
		}

		// Wait for snapshot creation to complete
		err = d.API.WaitForSnapshotStatus(
			ctx, sourceVolume, sourceSnapshot, api.SnapshotStatusAvailable, []string{api.SnapshotStatusError}, api.SnapshotTimeout)
		if err != nil {
			return err
		}

		// Save the snapshot in the volume config so we can auto-delete it later.
		cloneVolConfig.CloneSourceSnapshotInternal = sourceSnapshot.ID

		Logc(ctx).WithFields(LogFields{
			"snapshot": sourceSnapshot.Name,
			"source":   sourceVolume.Name,
		}).Debug("Created source snapshot.")
	}

	// TODO: (feat) RO clone support
	// TODO: (feat) volume labels support
	// TODO: (feat) multi-az support

	Logc(ctx).WithFields(LogFields{
		"creationToken":  name,
		"sourceVolume":   sourceVolume.CreationToken,
		"sourceSnapshot": sourceSnapshot.Name,
	}).Debug("Cloning volume.")

	_, _, sourceSnapshotID, err := api.ParseSnapshotID(sourceSnapshot.ID)
	if err != nil {
		return err
	}

	createRequest := &api.VolumeCreateRequest{
		ServiceID:      sourceVolume.ServiceID,
		Name:           cloneVolConfig.Name,
		MountPointName: name,
		// TODO: (feat) create export policy during volume creation
		// ExportPolicy:      sourceVolume.ExportPolicy,
		Protocol:        sourceVolume.Protocol,
		SizeInGigabytes: sourceVolume.SizeInGigabytes,
		SnapshotID:      sourceSnapshotID,
	}

	ACLs, err := d.API.ExportRulesForVolume(ctx, sourceVolume)
	if err != nil {
		return fmt.Errorf("could not get source volume export rules: %v", err)
	}
	var exportRule string
	if len(ACLs) >= 1 {
		exportRule = ACLs[0].AccessTo
		for i := 1; i < len(ACLs)-1; i++ {
			exportRule += fmt.Sprintf(",%s", ACLs[i].AccessTo)
		}
	}

	Logc(ctx).WithFields(LogFields{
		"creationToken":  name,
		"sourceVolume":   sourceVolume.ID,
		"sourceSnapshot": sourceSnapshot.Name,
		"request":        fmt.Sprintf("%+v", createRequest),
		"exportRule":     exportRule,
	}).Debug("Built clone volume request.")

	// Clone the volume
	clone, err := d.API.CreateVolume(ctx, createRequest)
	if err != nil {
		return err
	}

	// Always save the ID so we can retrieve the volume efficiently later
	cloneVolConfig.InternalID = clone.ID

	// Wait for creation to complete so that the mount targets are available
	return d.continueCreateVolume(ctx, clone, exportRule, cloneVolConfig)
}

// Import finds an existing volume and makes it available for containers. If ImportNotManaged is false, the
// volume is fully brought under Trident's management.
func (d *NASStorageDriver) Import(ctx context.Context, volConfig *storage.VolumeConfig, originalName string) error {
	fields := LogFields{
		"Method":       "Import",
		"Type":         "NFSStorageDriver",
		"originalName": originalName,
		"newName":      volConfig.InternalName,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> Import")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< Import")

	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return fmt.Errorf("could not update OVH resource cache; %v", err)
	}

	// Get the volume
	volume, err := d.API.VolumeByMountPointName(ctx, originalName)
	if err != nil {
		return fmt.Errorf("could not find volume %s; %v", originalName, err)
	}
	if volume.Status != api.VolumeStatusAvailable {
		return fmt.Errorf("volume %s is in state %s and is not available", originalName, volume.Status)
	}

	// Ensure the volume may be imported by a capacity pool managed by this backend
	if err = d.API.EnsureVolumeInValidCapacityPool(ctx, volume); err != nil {
		return err
	}

	// Get the volume size
	volConfig.Size = strconv.FormatInt(volume.SizeInGigabytes*1024*1024*1024, 10)

	Logc(ctx).WithFields(LogFields{
		"creationToken": volume.CreationToken,
		"managed":       !volConfig.ImportNotManaged,
		"state":         volume.Status,
		"capacityPool":  volume.ServiceID,
		"sizeBytes":     volConfig.Size,
	}).Debug("Found volume to import.")

	// Modify the volume if Trident will manage its lifecycle
	if !volConfig.ImportNotManaged {
		if volConfig.SnapshotDir != "" {
			if _, err = strconv.ParseBool(volConfig.SnapshotDir); err != nil {
				return fmt.Errorf("could not import volume %s, snapshot directory access is set to %s",
					originalName, volConfig.SnapshotDir)
			}
		}

		// TODO: (feat) volume labels support

		if d.Config.NASType == sa.SMB && volume.Protocol == api.ProtocolTypeCIFS {
			// SMB/CIFS is not supported
			return fmt.Errorf("could not import volume %s, SMB/CIFS protocol is not supported by this driver", originalName)
		} else if d.Config.NASType == sa.NFS && volume.Protocol == api.ProtocolTypeNFS {
			// NOTE: skipped modify for UNIX permissions,
			// snapshotDir acces and export rules

			// TODO: (feat) UNIX permissions
			// TODO: (feat) snapshot dir access
		} else {
			return fmt.Errorf("could not import volume '%s' due to backend and volume mismatch", originalName)
		}
	}

	// The volume ID cannot be changed, so it as the internal name
	volConfig.InternalName = originalName

	// Always save the ID, so we can find the volume efficiently later
	volConfig.InternalID = volume.ID

	return nil
}

func (d *NASStorageDriver) Rename(ctx context.Context, name, newName string) error {
	fields := LogFields{
		"Method":  "Rename",
		"Type":    "NASStorageDriver",
		"name":    name,
		"newName": newName,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> Rename")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< Rename")

	// Rename is only needed for the import workflow, and we aren't currently renaming the
	// OVHNF volume when importing, so do nothing here lest we set the volume name incorrectly
	// during an import failure cleanup.
	return nil
}

// getTelemetryLabels builds the standard telemetry labels that are set on each volume.
func (d *NASStorageDriver) getTelemetryLabels(ctx context.Context) string {
	telemetry := map[string]Telemetry{drivers.TridentLabelTag: *d.telemetry}

	telemetryJSON, err := json.Marshal(telemetry)
	if err != nil {
		Logc(ctx).Errorf("Failed to marshal telemetry: %+v", telemetry)
	}

	return strings.ReplaceAll(string(telemetryJSON), " ", "")
}

// updateTelemetryLabels updates a volume's labels to include the standard telemetry labels.
func (d *NASStorageDriver) updateTelemetryLabels(ctx context.Context, volume *api.Volume) map[string]string {
	panic("implement me")
}

// waitForVolumeCreate wait for volume creation to complete by reaching the Available state. If the
// vollume reaches a terminal state (Error), the volume is deleted. If the wait times out and the volume
// is still creating, a VolumeCreatingError is returned so the caller may try again.
func (d *NASStorageDriver) waitForVolumeCreate(ctx context.Context, volume *api.Volume) error {
	state, err := d.API.WaitForVolumeStatus(
		ctx, volume, api.VolumeStatusAvailable, []string{api.VolumeStatusError}, d.volumeCreateTimeout)
	if err != nil {
		logFields := LogFields{"volume": volume.ID}

		switch state {
		case api.VolumeStatusCreating, api.VolumesStatusCreatingFromSnapshot:
			Logc(ctx).WithFields(logFields).Debugf("Volume is in %s state.", state)
			return errors.VolumeCreatingError(err.Error())

		case api.VolumeStatusDeleting:
			// Don't wait if volume is already being deleted
			Logc(ctx).WithFields(logFields).WithError(err).Error(
				"Volume is being cleaned up and should be recreated later.")

		case api.VolumeStatusError:
			// Delete a failed volume
			errDelete := d.API.DeleteVolume(ctx, volume)
			if errDelete != nil {
				Logc(ctx).WithFields(logFields).Error(
					"Volume could not be cleaned up and must be manually deleted.")
				return multierr.Combine(err, errDelete)
			} else {
				Logc(ctx).WithFields(logFields).Info("Cleanup of failed volume started.")
			}

		case api.VolumeStatusReverting:
			fallthrough

		default:
			Logc(ctx).WithFields(logFields).Errorf("unexpected volume status %s found for volume", state)
		}
	}

	return err
}

// Destroy deletes a volume.
func (d *NASStorageDriver) Destroy(ctx context.Context, volConfig *storage.VolumeConfig) (err error) {
	name := volConfig.InternalName

	fields := LogFields{
		"Method": "Destroy",
		"Type":   "NASStorageDriver",
		"name":   name,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> Destroy")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< Destroy")

	// Update resource cache as needed
	if err = d.API.RefreshOVHResources(ctx); err != nil {
		return fmt.Errorf("could not update OVHNF resource cache; %v", err)
	}

	hasAutomaticSnapshot := false
	// If this volume was cloned from an automatic snapshot, delete the snapshot after deleting the volume.
	if volConfig.CloneSourceSnapshot == "" && volConfig.CloneSourceSnapshotInternal != "" {
		hasAutomaticSnapshot = true
		defer func() {
			d.deleteAutomaticSnapshot(ctx, err, volConfig)
		}()
	}

	// If volume doesn't exist, return success
	volumeExists, extantVolume, err := d.API.VolumeExists(ctx, volConfig)
	if err != nil {
		return fmt.Errorf("error checking for existing volume %s; %v", name, err)
	}
	if !volumeExists {
		Logc(ctx).WithField("volume", name).Warn("Volume already deleted.")
		return nil
	} else if extantVolume.Status == api.VolumeStatusDeleting {
		// This is a retry, so give it more time before giving up again. Only do this if the context is Docker or
		// if the CSI volume was created out of an automatic snapshot. Don't wait in other contexts.
		if d.Config.DriverContext == tridentconfig.ContextDocker || hasAutomaticSnapshot {
			_, err = d.API.WaitForVolumeStatus(
				ctx, extantVolume, api.VolumeStatusDeleted, []string{api.VolumeStatusError}, d.defaultTimeout())
			return err
		}
		return nil
	}

	// Delete the volume
	if err = d.API.DeleteVolume(ctx, extantVolume); err != nil {
		return err
	}

	Logc(ctx).WithField("volume", extantVolume.Name).Info("Volume deleted.")

	// If Docker or if the CSI volume was created out of an automatic snapshot, wait for volume deletion to complete.
	// Don't wait in other contexts.
	if d.Config.DriverContext == tridentconfig.ContextDocker || hasAutomaticSnapshot {
		// Wait for deletion to complete
		_, err = d.API.WaitForVolumeStatus(ctx, extantVolume, api.VolumeStatusDeleted, []string{api.VolumeStatusError}, d.defaultTimeout())
		return err

	}
	return nil
}

// deleteAutomaticSnapshot deletes a snapshot that was created automatically during volume clone creation.
// An automatic snapshot is detected by the presence of CloneSourceSnapshotInternal in the volume config
// while CloneSourceSnapshot is not set. This method is called after the volume has been deleted, and it
// will only attempt snapshot deletion if the clone volume deletion completed without error. This is a
// best-effort method, and any errors encountered will be logged but not returned.
func (d *NASStorageDriver) deleteAutomaticSnapshot(
	ctx context.Context, volDeleteError error, cloneVolConfig *storage.VolumeConfig,
) {
	snapshotID := cloneVolConfig.CloneSourceSnapshotInternal
	cloneSourceName := cloneVolConfig.CloneSourceVolumeInternal
	cloneName := cloneVolConfig.InternalID

	fields := LogFields{
		"Method":          "DeleteSnapshot",
		"Type":            "NASStorageDriver",
		"snapshotID":      snapshotID,
		"cloneSourceName": cloneSourceName,
		"cloneName":       cloneName,
	}

	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> deleteAutomaticSnasphot")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< deleteAutomaticSnapshot")

	logFields := LogFields{
		"snapshotID":      snapshotID,
		"cloneSourceName": cloneSourceName,
		"cloneName":       cloneName,
	}

	// Check if there's anything to do
	if !(cloneVolConfig.CloneSourceSnapshot == "" && cloneVolConfig.CloneSourceSnapshotInternal != "") {
		Logc(ctx).WithFields(logFields).Debug("No automatic clone source snapshot existed, skipping cleanup.")
		return
	}

	// If the clone volume couldn't be deleted, don't attempt to delete any automatic snapshot.
	if volDeleteError != nil {
		Logc(ctx).WithFields(logFields).Debug("Error deleting volume, skipping automatic snapshot cleanup.")
		return
	}

	// We always clone to the same capacity pool, so we can use the snapshot's info to get
	// capacity pool. Volume ID is part of snapshot's internal EFS name.
	cPoolName, volID, _, err := api.ParseSnapshotID(snapshotID)
	cloneSourceID := api.CreateVolumeID(cPoolName, volID)

	// Get the volume
	sourceVolume, err := d.API.VolumeByID(ctx, cloneSourceID)
	if err != nil {
		if errors.IsNotFoundError(err) {
			Logc(ctx).WithFields(logFields).WithError(err).Debug("Volume for automatic snapshot not found, skipping cleanup.")
		} else {
			Logc(ctx).WithFields(logFields).WithError(err).Error("Error checking for automatic snashot volume. " +
				"Any automatic snapshot must be manually deleted.")
		}
		return
	}

	// Get the snapshot
	sourceSnapshot, err := d.API.SnapshotByID(ctx, sourceVolume, snapshotID)
	if err != nil {
		// If the snapshot is already gone, return success
		if errors.IsNotFoundError(err) {
			Logc(ctx).WithFields(logFields).Debug("Automatic snapshot not found, skipping cleanup.")
		} else {
			Logc(ctx).WithFields(logFields).WithError(err).Error("Error checking for automatic snapshot. " +
				"Any automatic snapshot must be manually deleted.")
		}
		return
	}

	// Delete the snapshot
	if err = d.API.DeleteSnapshot(ctx, sourceVolume, sourceSnapshot); err != nil {
		Logc(ctx).WithFields(logFields).WithError(err).Errorf("Automatic snapshot could not be " +
			"cleaned up and must be manually deleted.")
	}

	return
}

// Publish the volume to the host specified in publishInfo.  This method may or may not be running on the host
// where the volume will be mounted, so it should limit itself to updating access rules, initiator groups, etc.
// that require some host identity (but not locality) as well as storage controller API access.
func (d *NASStorageDriver) Publish(ctx context.Context, volConfig *storage.VolumeConfig, publishInfo *models.VolumePublishInfo) error {
	name := volConfig.InternalName
	fields := LogFields{
		"Method": "Publish",
		"Type":   "NASStorageDriver",
		"name":   name,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> Publish")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< Publish")

	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return fmt.Errorf("could not update EFS resource cache; %v", err)
	}

	// TODO: (feat) RO clone support

	// Get the volume
	volume, err := d.API.Volume(ctx, volConfig)
	if err != nil {
		return fmt.Errorf("could not find volume '%s': %v", volConfig.InternalName, err)
	}

	// Get volume access paths
	paths, err := d.API.VolumeAccessPaths(ctx, volume)
	if err != nil {
		return fmt.Errorf("could not find volume '%s' access paths: %v", volConfig.InternalID, err)
	}

	if len(paths) == 0 {
		return fmt.Errorf("volume '%s' has no mount targets", volConfig.InternalID)
	}

	// Determine mount options (volume config wins, followed by backend config)
	mountOptions := d.Config.NFSMountOptions
	if volConfig.MountOptions != "" {
		mountOptions = volConfig.MountOptions
	}

	// Add required fields for attaching SMB volume
	if d.Config.NASType == sa.SMB {
		return fmt.Errorf("SMB/CIFS protocol is not supported by this driver")
	} else {
		// Add fields needed by Attach
		publishInfo.NfsServerIP = strings.Split(paths[0].Path, ":")[0]
		publishInfo.NfsPath = strings.Split(paths[0].Path, ":")[1]
		publishInfo.FilesystemType = sa.NFS
		publishInfo.MountOptions = mountOptions
	}

	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).
		WithFields(LogFields{
			"publishInfo":                fmt.Sprintf("%+v", publishInfo),
			"publishInfo.FileSystemType": publishInfo.FilesystemType,
			"publishInfo.NfsServerIP":    publishInfo.NfsServerIP,
			"publishInfo.NfsPath":        publishInfo.NfsPath,
			"mountOptions":               mountOptions,
		}).Trace("Populated publishInfo.")

	return nil
}

// CanSnapshot determines whether a snapshot as specified in the provided snapshot config may be taken.
func (d *NASStorageDriver) CanSnapshot(
	ctx context.Context, snapConfig *storage.SnapshotConfig, volConfig *storage.VolumeConfig,
) error {
	return nil
}

// GetSnapshot returns a snapshot of a volume, or an error if it does not exist.
func (d *NASStorageDriver) GetSnapshot(
	ctx context.Context, snapConfig *storage.SnapshotConfig, volConfig *storage.VolumeConfig,
) (*storage.Snapshot, error) {
	internalSnapName := snapConfig.InternalName
	internalVolID := volConfig.InternalID
	fields := LogFields{
		"Method":       "GetSnapshot",
		"Type":         "NASStorageDriver",
		"snapshotName": internalSnapName,
		"volumeID":     internalVolID,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> GetSnapshot")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< GetSnapshot")

	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return nil, fmt.Errorf("could not update EFS resource cache; %v", err)
	}

	// Get the volume
	volumeExists, extantVolume, err := d.API.VolumeExists(ctx, volConfig)
	if err != nil {
		return nil, fmt.Errorf("error checking for existing volume %s; %v", internalVolID, err)
	}
	if !volumeExists {
		// The OVH volume is backed by ONTAP, so if the volume doesn't exist, neither does the snapshot.
		Logc(ctx).WithFields(LogFields{
			"snapshotName": internalSnapName,
			"volumeID":     internalVolID,
		}).Debug("Volume for snapshot not found.")

		return nil, nil
	}

	// Get the snapshot
	snapshot, err := d.API.SnapshotForVolume(ctx, extantVolume, internalSnapName)
	if err != nil {
		if errors.IsNotFoundError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("could not check for existing snapshot; %v", err)
	}

	if snapshot.Status != api.SnapshotStatusAvailable {
		return nil, fmt.Errorf("snapshot '%s' status is '%s'", internalSnapName, snapshot.Status)
	}

	created := snapshot.CreatedAt.UTC().Format(convert.TimestampFormat)

	Logc(ctx).WithFields(LogFields{
		"snapshotName": internalSnapName,
		"volumeID":     internalVolID,
		"created":      created,
	}).Debug("Found snapshot.")

	return &storage.Snapshot{
		Config:    snapConfig,
		Created:   created,
		SizeBytes: 0,
		State:     storage.SnapshotStateOnline,
	}, nil
}

// GetSnapshots returns the list of snapshots associated with the specified volume.
func (d *NASStorageDriver) GetSnapshots(ctx context.Context, volConfig *storage.VolumeConfig) ([]*storage.Snapshot, error) {
	// internalVolID := volConfig.InternalID
	fields := LogFields{
		"Method":   "GetSnapshots",
		"Type":     "NASStorageDriver",
		"volumeID": volConfig.InternalID,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> GetSnapshots")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< GetSnapshots")

	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return nil, fmt.Errorf("could not update OVH resource cache; %v", err)
	}

	// Get the volume
	volume, err := d.API.Volume(ctx, volConfig)
	if err != nil {
		return nil, fmt.Errorf("could not find volume %s; %v", volConfig.InternalID, err)
	}

	snapshots, err := d.API.SnapshotsForVolume(ctx, volume)
	if err != nil {
		return nil, err
	}

	snapshotsList := make([]*storage.Snapshot, 0)

	for _, snapshot := range *snapshots {
		// Filter out snapshots is an unavailable state
		if snapshot.Status != api.SnapshotStatusAvailable {
			continue
		}

		snapshotsList = append(snapshotsList, &storage.Snapshot{
			Config: &storage.SnapshotConfig{
				Version:            tridentconfig.OrchestratorAPIVersion,
				Name:               snapshot.Name,
				InternalName:       snapshot.Name,
				VolumeName:         volConfig.Name,
				VolumeInternalName: volConfig.InternalName,
			},
			Created:   snapshot.CreatedAt.UTC().Format(convert.TimestampFormat),
			SizeBytes: 0,
			State:     storage.SnapshotStateOnline,
		})
	}

	return snapshotsList, nil
}

// CreateSnapshot creates a snapshot for the given volume.
func (d *NASStorageDriver) CreateSnapshot(
	ctx context.Context, snapConfig *storage.SnapshotConfig, volConfig *storage.VolumeConfig,
) (*storage.Snapshot, error) {
	internalSnapName := snapConfig.InternalName
	internalVolID := volConfig.InternalID
	fields := LogFields{
		"Method":       "CreateSnapshot",
		"Type":         "NASStorageDriver",
		"snapshotName": internalSnapName,
		"volumeID":     internalVolID,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> CreateSnapshot")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< CreateSnapshot")

	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return nil, fmt.Errorf("could not update OVH resource cache; %v", err)
	}

	// Check if volume exists
	volumeExists, sourceVolume, err := d.API.VolumeExists(ctx, volConfig)
	if err != nil {
		return nil, fmt.Errorf("error checking for existing volume %s; %v", internalVolID, err)
	}
	if !volumeExists {
		return nil, fmt.Errorf("volume %s does not exist", internalVolID)
	}

	// Create the snapshot
	snapshot, err := d.API.CreateSnapshot(ctx, sourceVolume, internalSnapName)
	if err != nil {
		return nil, fmt.Errorf("could not create snapshot; %v", err)
	}

	// Wait for snapshot creation to complete
	err = d.API.WaitForSnapshotStatus(
		ctx, sourceVolume, snapshot, api.SnapshotStatusAvailable, []string{api.SnapshotStatusError}, api.SnapshotTimeout)
	if err != nil {
		return nil, err
	}

	Logc(ctx).WithFields(LogFields{
		"snapshotName": snapConfig.InternalName,
		"volumeName":   snapConfig.VolumeInternalName,
		"volumeID":     volConfig.InternalID,
	}).Info("Snapshot created.")

	return &storage.Snapshot{
		Config:    snapConfig,
		Created:   snapshot.CreatedAt.UTC().Format(convert.TimestampFormat),
		SizeBytes: 0,
		State:     storage.SnapshotStateOnline,
	}, nil
}

// RestoreSnapshot restores a volume (in place) from a snapshot.
func (d *NASStorageDriver) RestoreSnapshot(
	ctx context.Context, snapConfig *storage.SnapshotConfig, volConfig *storage.VolumeConfig,
) error {
	internalSnapName := snapConfig.InternalName
	internalVolID := volConfig.InternalID
	fields := LogFields{
		"Method":       "RestoreSnapshot",
		"Type":         "NASStorageDriver",
		"snapshotName": internalSnapName,
		"volumeID":     internalVolID,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> RestoreSnapshot")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< RestoreSnapshot")

	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return fmt.Errorf("could not update OVH resource cache; %v", err)
	}

	// Get the volume
	volume, err := d.API.Volume(ctx, volConfig)
	if err != nil {
		return fmt.Errorf("could not find %s; %v", internalVolID, err)
	}

	// Get the snapshot
	snapshot, err := d.API.SnapshotForVolume(ctx, volume, internalSnapName)
	if err != nil {
		return fmt.Errorf("unable to find snapshot %s; %v", internalSnapName, err)
	}

	// Do the restore
	if err = d.API.RestoreSnapshot(ctx, volume, snapshot); err != nil {
		return err
	}

	// Wait for snapshot restoration to complete
	_, err = d.API.WaitForVolumeStatus(ctx, volume, api.VolumeStatusAvailable,
		[]string{api.VolumeStatusError, api.VolumeStatusRevertingError, api.VolumeStatusDeleting, api.VolumeStatusDeleted}, api.DefaultTimeout)

	return err
}

// DeleteSnapshot deletes a snapshot of a volume.
func (d *NASStorageDriver) DeleteSnapshot(
	ctx context.Context, snapConfig *storage.SnapshotConfig, volConfig *storage.VolumeConfig,
) error {
	internalSnapName := snapConfig.InternalName
	internalVolID := volConfig.InternalID
	fields := LogFields{
		"Method":       "DeleteSnapshot",
		"Type":         "NASStorageDriver",
		"snapshotName": internalSnapName,
		"volumeID":     internalVolID,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> DeleteSnapshot")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< DeleteSnapshot")

	// Update resource cache as neeeded
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return fmt.Errorf("could not update OVH resource cache; %v", err)
	}

	// Get the volume
	volumeExists, extantVolume, err := d.API.VolumeExists(ctx, volConfig)
	if err != nil {
		return fmt.Errorf("error checking for existing volume %s; %v", internalVolID, err)
	}
	if !volumeExists {
		// The OVH volume is backed by ONTAP, so if the volume doesn't exist, neither does the snapshot.
		Logc(ctx).WithFields(LogFields{
			"snapshotName": internalSnapName,
			"volumeName":   internalVolID,
		}).Debug("Volume for snapshot not found.")

		return nil
	}

	snapshot, err := d.API.SnapshotForVolume(ctx, extantVolume, internalSnapName)
	if err != nil {
		// If the snapshot is already gone, return success
		if errors.IsNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("unable to find snapshot %s; %v", internalSnapName, err)
	}

	if err = d.API.DeleteSnapshot(ctx, extantVolume, snapshot); err != nil {
		return err
	}

	// Wait for snapshot deletion to complete
	err = d.API.WaitForSnapshotStatus(
		ctx, extantVolume, snapshot, api.SnapshotStatusDeleted,
		[]string{api.SnapshotStatusError, api.SnapshotStatusErrorDeleting}, api.SnapshotTimeout,
	)

	return err
}

// List returns the list of volumes associated with this backend.
// TODO: how is it used
func (d *NASStorageDriver) List(ctx context.Context) ([]string, error) {
	fields := LogFields{"Method": "List", "Type": "NASStorageDriver"}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> List")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< List")

	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return nil, fmt.Errorf("could not update OVH resource cache; %v", err)
	}

	volumes, err := d.API.Volumes(ctx)
	if err != nil {
		return nil, err
	}

	prefix := *d.Config.StoragePrefix
	volumeNames := make([]string, 0)

	for _, volume := range volumes {
		// Filter out volumes in a unavailable state
		switch volume.Status {
		case api.VolumeStatusDeleting, api.VolumeStatusDeleted, api.VolumeStatusError:
			continue
		}

		// Filter out volumes without the prefix (pass all if prefix is empty)
		if !strings.HasPrefix(volume.CreationToken, prefix) {
			continue
		}

		volumeName := volume.CreationToken[len(prefix):]
		volumeNames = append(volumeNames, volumeName)
	}

	return volumeNames, nil
}

// Get tests for the existence of a volume.
func (d *NASStorageDriver) Get(ctx context.Context, name string) error {
	fields := LogFields{"Method": "Get", "Type": "NASStorageDriver"}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> Get")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< Get")

	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return fmt.Errorf("could not update OVH resource cache; %v", err)
	}

	if _, err := d.API.VolumeByMountPointName(ctx, name); err != nil {
		return fmt.Errorf("could not get volume %s; %v", name, err)
	}

	return nil
}

// Resize increses a volume's quota.
func (d *NASStorageDriver) Resize(ctx context.Context, volConfig *storage.VolumeConfig, sizeBytes uint64) error {
	name := volConfig.InternalName
	fields := LogFields{
		"Method":    "Resize",
		"Type":      "NASStorageDriver",
		"name":      name,
		"sizeBytes": sizeBytes,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> Resize")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< Resize")

	if sizeBytes > math.MaxInt64 {
		Logc(ctx).WithFields(fields).Error("Invalid volume size")
		return errors.New("invalid volume size")
	}

	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return fmt.Errorf("could not update GCNV resource cache; %v", err)
	}

	// Get the volume
	volume, err := d.API.Volume(ctx, volConfig)
	if err != nil {
		return fmt.Errorf("could not find volume %s; %v", name, err)
	}

	// If the volume state isn't "available", return an error
	if volume.Status != api.VolumeStatusAvailable {
		return fmt.Errorf("volume %s state is %s, not %s", name, volume.Status, api.VolumeStatusAvailable)
	}

	volSizeInBytes := volume.SizeInGigabytes * 1024 * 1024 * 1024
	volConfig.Size = strconv.FormatUint(uint64(volSizeInBytes), 10)

	// If the volume is already the requested size, there's nothing to do
	if int64(sizeBytes) == volSizeInBytes {
		return nil
	}

	// Make sure we're not shrinking the volume
	if int64(sizeBytes) < volSizeInBytes {
		return fmt.Errorf("requested size %d is less than existing volume size %d", sizeBytes, volSizeInBytes)
	}

	// Make sure the requests isn't above the configured maximum volume size (if any)
	if _, _, err = drivers.CheckVolumeSizeLimits(ctx, sizeBytes, d.Config.CommonStorageDriverConfig); err != nil {
		return err
	}

	// Resize the volume
	sizeBytesInGB := int64(sizeBytes) / (1 << 30)
	if err = d.API.ResizeVolume(ctx, volume, sizeBytesInGB); err != nil {
		return err
	}

	volConfig.Size = strconv.FormatUint(sizeBytes, 10)
	return nil
}

// GetStorageBackendSpecs retrieves storage capabilities and register pools with specified backend.
func (d *NASStorageDriver) GetStorageBackendSpecs(ctx context.Context, backend storage.Backend) error {
	backend.SetName(d.BackendName())

	for _, pool := range d.pools {
		pool.SetBackend(backend)
		backend.AddStoragePool(pool)
	}

	return nil
}

// CreatePrepare is called prior to volume creation. Currently, its only role is to create the internal volume name.
func (d *NASStorageDriver) CreatePrepare(ctx context.Context, volConfig *storage.VolumeConfig, storagePool storage.Pool) {
	volConfig.InternalName = d.GetInternalVolumeName(ctx, volConfig, storagePool)
}

// GetStorageBackendPhysicalPoolNames retrieves storage backend physical pools
func (d *NASStorageDriver) GetStorageBackendPhysicalPoolNames(ctx context.Context) []string {
	return []string{}
}

// getStorageBackendPools determines any non-overlapping, discrete storage pools present on a driver's storage backend
func (d *NASStorageDriver) getStorageBackendPools(ctx context.Context) []drivers.OVHNASStorageBackendPool {
	fields := LogFields{"Method": "getStorageBackendPools", "Type": "NASStorageDriver"}
	Logc(ctx).WithFields(fields).Debug(">>>> getStorageBackendPools")
	defer Logc(ctx).WithFields(fields).Debug("<<<< getStorageBackendPools")

	// For this driver, a discrete storage pool is composed of the following:
	// 1. Capacity Pool - contains exactly one pool per EFS service, service ID being unique within a given Region.

	// CapacityPoolsForStoragePools relies on a internal mapping of storage pools creted from the driver config.
	// If the behavior of that method should ever change, this method will need to change as well.
	cPools := d.API.CapacityPoolsForStoragePools(ctx)
	backendPools := make([]drivers.OVHNASStorageBackendPool, 0, len(cPools))
	for _, cPool := range cPools {
		backendPool := drivers.OVHNASStorageBackendPool{
			// Region:       d.Config.Region,
			// ServiceLevel: d.Config.ServiceLevel,
			CapacityPool: cPool.Name,
		}
		backendPools = append(backendPools, backendPool)
	}
	return backendPools
}

// GetInternalVolumeName accepts the name of a volume being created and returns what the internal name
// should be, depending on backend requirements and Trident's operating context.
func (d *NASStorageDriver) GetInternalVolumeName(ctx context.Context, volConfig *storage.VolumeConfig, storagePool storage.Pool) string {
	fields := LogFields{
		"Method": "GetInternalVolumeName",
		"Type":   "NASStorageDriver",
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> GetInternalVolumeName")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< GetInternalVolumeName")

	if tridentconfig.UsingPassthroughStore {
		// With a passthrough store, the name mapping must remain reversible
		return *d.Config.StoragePrefix + volConfig.Name
	} else if csiRegex.MatchString(volConfig.Name) {
		// If the name is from CSI (i.e. contains a UUID), just use it as-is
		// pvc-uuid
		Logc(ctx).WithField("volumeInternal", volConfig.Name).Debug("Using volume name as internal name.")
		return volConfig.Name
	} else {
		// EFS has strict limits on volume mount paths, so for cloud
		// infrastructure like Trident, the simplest approach is to generate a
		// UUID-based name with a prefix that won't exceed the 36-character limit.
		return "efs-" + uuid.NewString()
	}
}

// CreateFollowup is called after volume creation and sets the access info in the volume config.
func (d *NASStorageDriver) CreateFollowup(ctx context.Context, volConfig *storage.VolumeConfig) error {
	var volume *api.Volume
	var err error

	name := volConfig.InternalName
	fields := LogFields{
		"Method":                 "CreateFollowup",
		"Type":                   "NASStorageDriver",
		"volConfig.Internalname": name,
		"volConfig.InternalID":   volConfig.InternalID,
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> CreateFollowup")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< CreateFollowup")

	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return fmt.Errorf("could not update OVH resource cache; %v", err)
	}

	// TODO: (feat) RO clone support

	// Get the volume
	volume, err = d.API.Volume(ctx, volConfig)
	if err != nil {
		return fmt.Errorf("could not find volume %s; %v", name, err)
	}

	// Ensure volume is in a good state
	if volume.Status != api.VolumeStatusAvailable {
		return fmt.Errorf("volume %s is in %s state, not %s", name, volume.Status, api.VolumeStatusAvailable)
	}

	// Get mount targets for volume
	mountTargets, err := d.API.VolumeAccessPaths(ctx, volume)
	if err != nil {
		return fmt.Errorf("could not get volume mount targets %s; %v", volConfig.InternalID, err)
	}

	if len(mountTargets) == 0 {
		return fmt.Errorf("volume %s has no mount targets", volConfig.InternalID)
	}

	// Set the mount target based on the NASType
	if d.Config.NASType == sa.SMB {
		return fmt.Errorf("SMB/CIFS protocol is not supported by this driver")
	} else if d.Config.NASType == sa.NFS {
		// Use the first mount target found
		volConfig.AccessInfo.NfsServerIP = strings.Split(mountTargets[0].Path, ":")[0]
		volConfig.AccessInfo.NfsPath = strings.Split(mountTargets[0].Path, ":")[1]
		volConfig.FileSystem = sa.NFS
	} else {
		return fmt.Errorf("volume NAS type %s is not supported", d.Config.NASType)
	}

	return nil
}

// GetProtocol returns the protocol supported by this driver (File).
func (d *NASStorageDriver) GetProtocol(ctx context.Context) tridentconfig.Protocol {
	return tridentconfig.File
}

// StoreConfig add this backend's config to the persistent config struct, as needed by Trident's persistence layer.
func (d *NASStorageDriver) StoreConfig(ctx context.Context, b *storage.PersistentStorageBackendConfig) {
	drivers.SanitizeCommonStorageDriverConfig(d.Config.CommonStorageDriverConfig)
	b.OVHConfig = &d.Config
}

// GetExternalConfig returns a clone of this backend's config, sanitized for external consumption.
func (d *NASStorageDriver) GetExternalConfig(ctx context.Context) interface{} {
	// Clone the config so we don't risk altering the original
	var cloneConfig drivers.OVHNASStorageDriverConfig
	drivers.Clone(ctx, d.Config, &cloneConfig)
	// redact the credentials
	cloneConfig.ClientID = tridentconfig.REDACTED
	cloneConfig.ClientSecret = tridentconfig.REDACTED
	return cloneConfig
}

// GetVolumeForImport queries the storage backend for all relevant info about
// a single container volume managed by this driver and returns a VolumeExternal
// representation of the volume. For this driver, volumeID is the unique name
// used during volume creation.
func (d *NASStorageDriver) GetVolumeForImport(ctx context.Context, volumeID string) (*storage.VolumeExternal, error) {
	// Update resource cache as needed
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		return nil, fmt.Errorf("could not update OVH resource cache; %v", err)
	}

	volume, err := d.API.VolumeByMountPointName(ctx, volumeID)
	if err != nil {
		return nil, err
	}

	return d.getVolumeExternal(volume), nil
}

// GetVolumeExternalWrappers queries the storage backend for all relevant info about
// a single container volume managed by this driver. It then writes a VolumeExternal
// representation of each volume to the supplied channel, closing the channel
// when finished.
func (d *NASStorageDriver) GetVolumeExternalWrappers(ctx context.Context, channel chan *storage.VolumeExternalWrapper) {
	fields := LogFields{"Method": "GetVolumeExternalWrappers", "Type": "NASStorageDriver"}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> GetVolumeExternalWrappers")
	defer Logd(ctx, d.Name(),
		d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< GetVolumeExternalWrappers")

	// Let the caller know we're done by closing the channel
	defer close(channel)

	// Update the resource cache as neeeded
	if err := d.API.RefreshOVHResources(ctx); err != nil {
		channel <- &storage.VolumeExternalWrapper{Volume: nil, Error: err}
		return
	}

	// Get all volumes
	volumes, err := d.API.Volumes(ctx)
	if err != nil {
		channel <- &storage.VolumeExternalWrapper{Volume: nil, Error: err}
		return
	}

	prefix := *d.Config.StoragePrefix

	// Convert all volumes to VolumeExternal and write them to the channel
	for _, volume := range volumes {
		// Filter out volumes in an unavailable state
		switch volume.Status {
		case api.VolumeStatusDeleting, api.VolumeStatusDeleted, api.VolumeStatusError, api.VolumeStatusExtendingError, api.VolumeStatusShrinkingError, api.VolumeStatusRevertingError:
			continue
		}

		// Filter out volumes without the prefix (pass all if prefix is empty)
		if !strings.HasPrefix(volume.CreationToken, prefix) {
			continue
		}

		channel <- &storage.VolumeExternalWrapper{Volume: d.getVolumeExternal(volume), Error: nil}
	}
}

// getVolumeExternal is a private method that accepts info about a volume
// as returned by the storage backend and formats it as a VolumeExternal
// object.
func (d *NASStorageDriver) getVolumeExternal(volumeAttrs *api.Volume) *storage.VolumeExternal {
	volumeConfig := &storage.VolumeConfig{
		Version:         tridentconfig.OrchestratorAPIVersion,
		Name:            volumeAttrs.Name,
		InternalName:    volumeAttrs.CreationToken,
		InternalID:      volumeAttrs.ID,
		Size:            strconv.FormatInt(volumeAttrs.SizeInGigabytes*1024*1024*1024, 10),
		Protocol:        tridentconfig.File,
		SnapshotPolicy:  "",
		ExportPolicy:    "",
		SnapshotDir:     defaultSnapshotDir, // TODO: (feat) snapshot dir access
		UnixPermissions: "",                 // TODO: (feat) UNIX permissions
		StorageClass:    "",
		AccessMode:      tridentconfig.ReadWriteMany,
		AccessInfo:      models.VolumeAccessInfo{},
		BlockSize:       "",
		FileSystem:      "",
		ServiceLevel:    defaultServiceLevel, // We have only one service level
	}

	return &storage.VolumeExternal{
		Config: volumeConfig,
		Pool:   drivers.UnsetPool,
	}
}

// GetUpdateType returns a bitmap populated with updates to the driver.
func (d *NASStorageDriver) GetUpdateType(ctx context.Context, driverOrig storage.Driver) *roaring.Bitmap {
	bitmap := roaring.New()
	dOrig, ok := driverOrig.(*NASStorageDriver)
	if !ok {
		bitmap.Add(storage.InvalidUpdate)
		return bitmap
	}

	if !reflect.DeepEqual(d.Config.StoragePrefix, dOrig.Config.StoragePrefix) {
		bitmap.Add(storage.PrefixChange)
	}

	if !drivers.AreSameCredentials(d.Config.Credentials, dOrig.Config.Credentials) {
		bitmap.Add(storage.CredentialsChange)
	}

	return bitmap
}

// ReconcileNodeAccess updates a per-backend export policy to match the set of Kubernetes cluster
// nodes. Not supported by this driver.
func (d *NASStorageDriver) ReconcileNodeAccess(ctx context.Context, _ []*models.Node, _, _ string) error {
	fields := LogFields{
		"Method": "ReconcileNodeAccess",
		"Type":   "NFSStorageDriver",
	}
	Logd(ctx, d.Config.StorageDriverName, d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> ReconcileNodeAccess")
	defer Logd(ctx, d.Config.StorageDriverName, d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< ReconcileNodeAccess")

	return nil
}

func (d *NASStorageDriver) ReconcileVolumeNodeAccess(ctx context.Context, _ *storage.VolumeConfig, _ []*models.Node) error {
	fields := LogFields{
		"Method": "ReconcileVolumeNodeAccess",
		"Type":   "NASStorageDriver",
	}
	Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace(">>>> ReconcileVolumeNodeAccess")
	defer Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).Trace("<<<< ReconcileVolumeNodeAccess")

	return nil
}

func validateStoragePrefix(storagePrefix string) error {
	if !storagePrefixRegex.MatchString(storagePrefix) {
		return fmt.Errorf("storage prefix may only contain letters and hyphens and must begin with a letter")
	}
	return nil
}

// GetCommonConfig returns driver's GetCommonConfig
func (d *NASStorageDriver) GetCommonConfig(ctx context.Context) *drivers.CommonStorageDriverConfig {
	return d.Config.CommonStorageDriverConfig
}

func (d *NASStorageDriver) continueCreateVolume(ctx context.Context, volume *api.Volume,
	exportRule string, config *storage.VolumeConfig,
) error {
	fields := LogFields{
		"Method":    "continueCreateVolume",
		"Type":      "NASStorageDriver",
		"volConfig": fmt.Sprintf("%+v", config),
		"volume":    fmt.Sprintf("%+v", volume),
	}
	Logc(ctx).WithFields(fields).Info("confinueCreateVolume started")

	err := d.waitForVolumeCreate(ctx, volume)
	if err != nil {
		return err
	}

	// Create export rules
	g := new(errgroup.Group)

	for _, rule := range strings.Split(exportRule, ",") {
		request := &api.ExportRuleCreateRequest{
			AccessLevel: api.AccessReadWrite,
			AccessTo:    rule,
		}
		Logd(ctx, d.Name(), d.Config.DebugTraceFlags["method"]).WithFields(fields).
			WithField("exportRuleRequest", fmt.Sprintf("%+v", request)).Trace("Prepared request for export rule.")
		g.Go(func() error {
			exportRule, err := d.API.CreateExportRule(ctx, volume, request)
			if err != nil {
				return err
			}

			return d.waitForExportRuleCreate(ctx, volume, exportRule)
		})
	}

	// Wait for all export rules creations to complete
	if err := g.Wait(); err != nil {
		return err
	}

	return nil
}

func (d *NASStorageDriver) waitForExportRuleCreate(ctx context.Context, volume *api.Volume, exportRule *api.ExportRule) error {
	state, err := d.API.WaitForExportRuleStatus(
		ctx, volume, exportRule, api.ExportRuleStatusActive, []string{api.ExportRuleStatusError}, d.volumeCreateTimeout)
	if err != nil {
		logFields := LogFields{"volume": volume.ID, "exportRule": exportRule.ID}

		switch state {
		case api.ExportRuleStatusApplying, api.ExportRuleStatusQueuedToApply:

			Logc(ctx).WithFields(logFields).Debugf("Export rule is in %s state.", state)
			return errors.VolumeCreatingError(err.Error())

		case api.ExportRuleStatusDenying:
			// Don't wait if export rule is already being deleted
			Logc(ctx).WithFields(logFields).WithError(err).Error(
				"Export rule is being cleaned up and should be recreated later.")

		case api.ExportRuleStatusError:
			// Delete a failed export rule
			if errDelete := d.API.DeleteExportRule(ctx, volume, exportRule); errDelete != nil {
				Logc(ctx).WithFields(logFields).WithError(errDelete).Error("Export rule could not be cleaned up and must be manually deleted: %v.", errDelete)
			} else {
				Logc(ctx).WithFields(logFields).Info("Cleanup of failed export rule started.")
			}

			Logc(ctx).WithFields(logFields).Debugf("Export rule is in %s state.", state)
			return err

		default:
			Logc(ctx).WithFields(logFields).Errorf("unexpected state %s found for export rule", state)
		}
	}

	return err
}
