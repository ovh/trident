// Package api provides a high-level interface to the OVH EFS REST API.
package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/ovh/go-ovh/ovh"

	tridentConfig "github.com/netapp/trident/config"
	. "github.com/netapp/trident/logging"
	"github.com/netapp/trident/pkg/collection"
	"github.com/netapp/trident/storage"
	"github.com/netapp/trident/utils/errors"
)

const (
	VolumeCreateTimeout = 300 * time.Second
	DefaultTimeout      = 120 * time.Second
)

var (
	volumeIDRegex   = regexp.MustCompile(`^/storage/netapp/(?P<serviceName>[^/]+)/share/(?P<share>[^/]+)$`)
	volumeNameRegex = regexp.MustCompile(`^/storage/netapp/(?P<serviceName>[^/]+)/share/(?P<share>[^/]+)$`)
	snapshotIDRegex = regexp.MustCompile(`^/storage/netapp/(?P<serviceName>[^/]+)/share/(?P<share>[^/]+)/snapshot/(?P<snapshot>[^/]+)$`)
)

// ClientConfig holds configuration data for the API driver object.
type ClientConfig struct {
	StorageDriverName string

	// EFS region
	Location string

	// OVH API OAuth2 authentication parameters
	ClientID       string
	ClientSecret   string
	ClientLocation string

	// Options
	DebugTraceFlags map[string]bool
	MaxCacheAge     time.Duration // The oldest data we should expect in the cached resources
	APITimeout      time.Duration // Timeout applied to all calls to the OVH API
}

type OVHEFSClient struct {
	httpClient *ovh.Client
	OVHResources
}

// Client encapsulates connection details.
type Client struct {
	config    *ClientConfig
	sdkClient *OVHEFSClient
}

// NewDriver is a factory method for creating a new instance.
func NewDriver(config *ClientConfig) (OVHClient, error) {
	// Ensure we got a location
	if config.Location == "" || config.ClientLocation == "" {
		return nil, errors.New("both API client location and location must be specified in configuration")
	}

	httpClient, err := ovh.NewOAuth2Client(config.ClientLocation, config.ClientID, config.ClientSecret)
	if err != nil {
		return nil, err
	}

	// Set User-Agent to Trident version
	httpClient.UserAgent = "Trident/" + tridentConfig.OrchestratorVersion.String()

	sdkClient := &OVHEFSClient{
		httpClient: httpClient,
	}

	return Client{
		config:    config,
		sdkClient: sdkClient,
	}, nil
}

// Init runs startup logic after allocating the driver resources.
func (c Client) Init(ctx context.Context, pools map[string]storage.Pool) error {
	// Map vpools to backend
	c.registerStoragePools(pools)

	// Find out what we have to work with in OVH
	return c.RefreshOVHResources(ctx)
}

// registerStoragePools makes a note of pools defined by the driver for later mapping.
func (c Client) registerStoragePools(sPools map[string]storage.Pool) {
	c.sdkClient.OVHResources.StoragePoolMap = make(map[string]storage.Pool)

	for _, sPool := range sPools {
		c.sdkClient.StoragePoolMap[sPool.Name()] = sPool
	}
}

// ///////////////////////////////////////////////////////////////////////////////
// Functions to create & parse OVH EFS resource IDs and names
// ///////////////////////////////////////////////////////////////////////////////

/*
   This shouldn't be needed as we don't have more fields that capacity pool name
func CreateCapacityPoolFullName(capacityPool string) string {
	return fmt.Sprintf("%s", capacityPool)
        }
*/

// CreateVolumeID creates the OVH-style ID for a volume.
func CreateVolumeID(capacityPool, volume string) string {
	return fmt.Sprintf("/storage/netapp/%s/share/%s", capacityPool, volume)
}

// CreateVolumeFullName create the fully qualified name for a volume.
func CreateVolumeFullName(capacityPool, volume string) string {
	return fmt.Sprintf("/storage/netapp/%s/share/%s", capacityPool, volume)
}

// ParseVolumeID parses the OVH-style ID for a volume.
func ParseVolumeID(volumeID string) (capacityPool, volume string, err error) {
	match := volumeIDRegex.FindStringSubmatch(volumeID)

	if match == nil {
		err = fmt.Errorf("volume ID %s is invalid", volumeID)
		return
	}

	paramsMap := make(map[string]string)
	for i, name := range volumeIDRegex.SubexpNames() {
		if i > 0 && i <= len(match) {
			paramsMap[name] = match[i]
		}
	}

	capacityPool = paramsMap["serviceName"]
	volume = paramsMap["share"]

	return
}

// ParseVolumeName parses the OVH-style Name for a volume.
func ParseVolumeName(volumeName string) (capacityPool, volume string, err error) {
	match := volumeNameRegex.FindStringSubmatch(volumeName)

	if match == nil {
		err = fmt.Errorf("volume name %s is invalid", volumeName)
	}

	paramsMap := make(map[string]string)
	for i, name := range volumeNameRegex.SubexpNames() {
		if i > 0 && i <= len(match) {
			paramsMap[name] = match[i]
		}
	}

	capacityPool = paramsMap["serviceName"]
	volume = paramsMap["share"]

	return
}

// CreateSnapshotID creates the OVH-style ID for a snapshot.
func CreateSnapshotID(capacityPool, volume, snapshot string) string {
	return fmt.Sprintf("/storage/netapp/%s/share/%s/snapshot/%s", capacityPool, volume, snapshot)
}

// ParseSnapshotID parses the OVH-style ID for a snapshot.
func ParseSnapshotID(snapshotID string) (capacityPool, volume, snapshot string, err error) {
	match := snapshotIDRegex.FindStringSubmatch(snapshotID)

	if match == nil {
		err = fmt.Errorf("snapshot ID %s is invalid", snapshotID)
		return
	}

	paramsMap := make(map[string]string)
	for i, name := range snapshotIDRegex.SubexpNames() {
		if i > 0 && i <= len(match) {
			paramsMap[name] = match[i]
		}
	}

	capacityPool = paramsMap["serviceName"]
	volume = paramsMap["share"]
	snapshot = paramsMap["snapshot"]

	return
}

func (c *Client) makeURL(storagePoolID, resourcePath string) string {
	url := fmt.Sprintf("/storage/netapp/%s%s", storagePoolID, resourcePath)
	return url
}

// newVolumeFromEFSVolume creates a new internal Volume struct from a EFS volume.
func (c Client) newVolumeFromEFSVolume(_ context.Context, volume *EFSVolume) (*Volume, error) {
	if volume == nil {
		return nil, errors.New("nil volume")
	}

	cPoolID, _, err := ParseVolumeID(volume.ID)
	if err != nil {
		return nil, err
	}

	return &Volume{
		ID:              volume.ID,
		CreatedAt:       volume.CreatedAt,
		Name:            volume.Name,
		ServiceID:       cPoolID,
		SizeInGigabytes: volume.SizeInGigabytes,
		Status:          volume.Status,
		// MountPointName:   volume.MountPointName,
		Protocol: volume.Protocol,
	}, nil
}

// Volumes returns a list of all volumes.
func (c Client) Volumes(ctx context.Context) ([]*Volume, error) {
	logFields := LogFields{
		"API": "OVH.ListVolumes",
	}

	var volumes []*Volume

	resourcePath := "/share"

	cPools := c.CapacityPools()

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()
	// We iterate over all storage pools to get volumes from it.
	for _, cPool := range *cPools {
		var efsVolumes []*EFSVolume
		err := c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodGet, c.makeURL(cPool.ID, resourcePath), nil, &efsVolumes, true)
		if err != nil {
			Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
				WithFields(logFields).WithError(err).
				Errorf("Error reading volumes from pool %s.", cPool.ID)
			return nil, err
		}

		Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
			WithFields(logFields).WithField("count", len(volumes)).
			Debug("Read volumes from pool %s.", cPool.ID)

		for _, efsVol := range efsVolumes {
			efsVol.ID = CreateVolumeID(cPool.ID, efsVol.ID)
			volume, err := c.newVolumeFromEFSVolume(ctx, efsVol)
			if err != nil {
				Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
					WithFields(logFields).WithError(err).
					Warning("Skipping volume.")
				continue
			}
			volumes = append(volumes, volume)
		}

	}

	return volumes, nil
}

// Volume uses a volume config record to fetch a volume by the most efficient means.
func (c Client) Volume(ctx context.Context, volConfig *storage.VolumeConfig) (*Volume, error) {
	// When we know the internal ID, use that as it is more efficient
	if volConfig.InternalID != "" {
		return c.VolumeByID(ctx, volConfig.InternalID)
	}

	// Fall back to the mount point name
	return c.VolumeByMountPointName(ctx, volConfig.InternalName)
}

// VolumeByID returns a Volume based on its OVH-style ID.
func (c Client) VolumeByID(ctx context.Context, id string) (*Volume, error) {
	logFields := LogFields{
		"API":    "OVH.GetVolume",
		"volume": id,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Fetching volume by ID.")

	cPoolID, volumeID, err := ParseVolumeID(id)
	if err != nil {
		return nil, err
	}

	resourcePath := fmt.Sprintf("/share/%s", volumeID)

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	var volume EFSVolume
	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodGet, c.makeURL(cPoolID, resourcePath), nil, &volume, true)
	if err != nil {
		if IsOVHNotFoundError(err) {
			Logc(ctx).WithFields(logFields).Debug("Volume not found.")
			return nil, errors.WrapWithNotFoundError(err, "volume with ID '%s' not found", volumeID)
		}

		Logc(ctx).WithError(err).Debug("Failed to get volume by ID.")
		return nil, err
	}

	Logc(ctx).WithFields(logFields).Debug("Found volume by ID.")

	volume.ID = id
	return c.newVolumeFromEFSVolume(ctx, &volume)
}

// VolumeByMountPointName fetches a volume by its immutable mount point name. We can't query
// volumes from multiple EFS services at once so we go through each capacity pool / EFS service.
func (c Client) VolumeByMountPointName(ctx context.Context, mountPointName string) (*Volume, error) {
	logFields := LogFields{
		"API":            "OVH.GetVolume",
		"mountPointName": mountPointName,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Tracef("Fetching volume by mount point name.")

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	var volumes []*Volume
	var err error
	for _, cPool := range c.sdkClient.CapacityPoolMap {
		var matchingVolumes []*EFSVolume

		// Mount point name is used as unique token as a service cannot have
		// the same mount point name twice.
		resourcePath := fmt.Sprintf("/share?detail=true&mountPointName=%s", url.QueryEscape(mountPointName))

		err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodGet, c.makeURL(cPool.ID, resourcePath), nil, &matchingVolumes, true)
		if err != nil {
			Logc(ctx).WithFields(logFields).WithError(err).
				Error("Error fetching volume.")
			continue
		}

		if len(matchingVolumes) > 0 {
			// Compute volume IDs for matching volumes
			for _, efsVol := range matchingVolumes {
				efsVol.ID = CreateVolumeID(cPool.ID, efsVol.ID)
				volume, err := c.newVolumeFromEFSVolume(ctx, efsVol)
				if err != nil {
					Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
						WithFields(logFields).WithError(err).Warning("Skipping volume.")
					continue
				}

				volumes = append(volumes, volume)
			}
		}

	}

	if len(volumes) == 0 {
		return nil, errors.NotFoundError("volume with mount point name '%s' not found", mountPointName)
	} else if len(volumes) > 1 {
		return nil, errors.New("found multiple volumes with the same mount point name in given storage pools")
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Debug("Found volume with mount point name.")
	return volumes[0], nil
}

// VolumeExists uses a volume config to look for a Volume by the most efficient means.
func (c Client) VolumeExists(ctx context.Context, volConfig *storage.VolumeConfig) (bool, *Volume, error) {
	// When we know the internal ID, use that as it is vastly more efficient
	if volConfig.InternalID != "" {
		return c.VolumeExistsByID(ctx, volConfig.InternalID)
	}

	// Fall back to the mount point name
	return c.VolumeExistsByMountPointName(ctx, volConfig.InternalName)
}

// VolumeExistsByID checks whether a volume exists using its ID as a key.
func (c Client) VolumeExistsByID(ctx context.Context, id string) (bool, *Volume, error) {
	if vol, err := c.VolumeByID(ctx, id); err != nil {
		if errors.IsNotFoundError(err) {
			return false, nil, nil
		}
		return false, nil, err
	} else {
		return true, vol, nil
	}
}

// VolumeExistsByMountPointName checks whether a volume exists using its mount point name as key.
func (c Client) VolumeExistsByMountPointName(ctx context.Context, mountPointName string) (bool, *Volume, error) {
	if vol, err := c.VolumeByMountPointName(ctx, mountPointName); err != nil {
		if errors.IsNotFoundError(err) {
			return false, nil, nil
		} else {
			return false, nil, err
		}
	} else {
		return true, vol, nil
	}
}

// ExportRulesExists checks whether export rules exists on volume.
// IPs are compared to IPs and CIDRs to CIRDs.
func (c Client) ExportRulesExists(ctx context.Context, volume *Volume, exportRules string) (bool, []*ExportRule, error) {
	logFields := LogFields{
		"API":         "OVH.ListExportRules",
		"volume":      volume.ID,
		"exportRules": exportRules,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Checking matching export rules existence.")

	// Volume Export Rules is a list of entries (either single IP or CIDR)
	volumeExportRules, err := c.ExportRulesForVolume(ctx, volume)
	if err != nil {
		Logc(ctx).WithFields(logFields).WithError(err).
			Error("Error fetching volume export rules.")
		return false, nil, err

	}

	volExpRulesMap := make(map[string]*ExportRule)
	for _, volExpRule := range volumeExportRules {
		volExpRulesMap[volExpRule.AccessTo] = volExpRule
	}

	matchingExportRules := []*ExportRule{}

	// A rule is either single IP or CIDR
	rules := strings.Split(exportRules, ",")
	for _, rule := range rules {
		// Skip empty rules
		if rule == "" {
			continue
		}

		ipAddr := net.ParseIP(rule)
		_, netAddr, _ := net.ParseCIDR(rule)

		// Access is always RW
		if ipAddr != nil {
			if volRule, ok := volExpRulesMap[ipAddr.String()]; ok && volRule.AccessLevel == AccessReadWrite {
				// Match two IP addresses.
				// Note: This do not takes into account the case when ipAddr is contained
				// inside a CIDR covered by an existing rule.
				Logc(ctx).WithFields(logFields).WithFields(LogFields{
					"volumeExportRule": ipAddr,
				}).Debug("Found IP address match.")
				matchingExportRules = append(matchingExportRules, volExpRulesMap[ipAddr.String()])
				continue
			}
		}

		if netAddr != nil {
			if volRule, ok := volExpRulesMap[netAddr.String()]; ok && volRule.AccessLevel == AccessReadWrite {
				// Matching two CIDR(s).
				Logc(ctx).WithFields(logFields).WithFields(LogFields{
					"volumeExportRule": netAddr,
				}).Debug("Found CIDR match.")
				matchingExportRules = append(matchingExportRules, volExpRulesMap[netAddr.String()])
				continue
			}
		}

		return false, nil, nil
	}

	return true, matchingExportRules, nil
}

// WaitForVolumeStatus watches for a desired volume status and returns when that status is reached.
func (c Client) WaitForVolumeStatus(
	ctx context.Context, volume *Volume, desiredStatus string, abortStatuses []string,
	maxElapsedTime time.Duration,
) (string, error) {
	volumeState := ""

	checkVolumeState := func() error {
		f, err := c.VolumeByID(ctx, volume.ID)
		if err != nil {
			// There's not `deleted` state in EFS -- volume just vanishes. If we failed to query
			// the volume info, and we're trying to transition to StatusDeleted, and we get back a HTTP 404,
			// then return success. Otherwise, log the error as usual.
			if desiredStatus == VolumeStatusDeleted && errors.IsNotFoundError(err) {
				Logc(ctx).Debugf("Implied deletion for volume %s.", volume.Name)
				volumeState = VolumeStatusDeleted
				return nil
			}

			if errors.Is(err, context.Canceled) {
				return backoff.Permanent(err)
			}

			volumeState = ""
			return fmt.Errorf("could not get volume status; %v", err)
		}

		volumeState = f.Status

		if desiredStatus == volumeState {
			Logc(ctx).WithFields(LogFields{
				"desiredStatus": desiredStatus,
				"volume":        volume.ID,
			}).Debug("Desired volume status reached.")
			return nil
		}

		err = fmt.Errorf("volume state is %s, not %s", f.Status, desiredStatus)

		// Return a permanent error to stop retrying if we reached one of the abort states
		if collection.ContainsString(abortStatuses, f.Status) {
			return backoff.Permanent(TerminalState(err))
		}

		return err
	}

	stateNotify := func(err error, duration time.Duration) {
		Logc(ctx).WithFields(LogFields{
			"increment": duration.Truncate(10 * time.Millisecond),
			"message":   err.Error(),
		}).Debug("Waiting for volume status.")
	}

	stateBackoff := backoff.NewExponentialBackOff()
	stateBackoff.MaxElapsedTime = maxElapsedTime
	stateBackoff.MaxInterval = 5 * time.Second
	stateBackoff.RandomizationFactor = 0.1
	stateBackoff.InitialInterval = backoff.DefaultInitialInterval
	stateBackoff.Multiplier = 1.414

	Logc(ctx).WithField("desiredStatus", desiredStatus).Info("Waiting for volume status.")

	if err := backoff.RetryNotify(checkVolumeState, stateBackoff, stateNotify); err != nil {
		if IsTerminalStateError(err) {
			Logc(ctx).Errorf("Volume reached terminal status.")
		} else {
			Logc(ctx).Errorf("Volume state was not any of %s after %3.2f seconds.",
				[]string{desiredStatus}, stateBackoff.MaxElapsedTime.Seconds())
		}
		return volumeState, err
	}

	Logc(ctx).WithField("desiredStatus", desiredStatus).Debug("Desired volume status reached.")

	return volumeState, nil
}

// CreateVolume creates a new volume.
func (c Client) CreateVolume(ctx context.Context, request *VolumeCreateRequest) (*Volume, error) {
	resourcePath := "/share"

	cPoolName := request.ServiceID

	// Get the capacity pool
	cPoolFullName := cPoolName
	_, ok := c.sdkClient.OVHResources.CapacityPoolMap[cPoolFullName]
	if !ok {
		return nil, fmt.Errorf("unknown capacity pool %s", request.ServiceID)
	}

	Logc(ctx).WithFields(LogFields{
		"name":          request.Name,
		"creationToken": request.MountPointName,
		"capacityPool":  cPoolName,
		"snapshotID":    request.SnapshotID,
	}).Debug("Issuing create request.")

	logFields := LogFields{
		"API":           "OVH.CreateVolume",
		"volume":        request.Name,
		"creationToken": request.MountPointName,
		"capacityPool":  cPoolName,
	}

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	var vol EFSVolume
	err := c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodPost, c.makeURL(request.ServiceID, resourcePath), request, &vol, true)
	if err != nil {
		Logc(ctx).WithFields(logFields).WithError(err).Error("Error creating volume.")
		return nil, err
	}

	Logc(ctx).WithFields(logFields).WithFields(LogFields{
		"volume": vol.ID,
	}).Info("Volume create request issued.")

	// Forge volume ID from returned ID.
	newVolID := CreateVolumeID(cPoolName, vol.ID)
	vol.ID = newVolID

	return c.newVolumeFromEFSVolume(ctx, &vol)
}

// ResizeVolume extends or shrinks a volume.
func (c Client) ResizeVolume(ctx context.Context, volume *Volume, newSizeGigabytes int64) error {
	logFields := LogFields{
		"API":    "OVH.ResizeVolume",
		"volume": volume.ID,
	}

	cPoolID, volID, err := ParseVolumeID(volume.ID)
	if err != nil {
		return err
	}

	var resourcePath string
	if newSizeGigabytes == volume.SizeInGigabytes {
		return nil
	} else if newSizeGigabytes > volume.SizeInGigabytes {
		resourcePath = fmt.Sprintf("/share/%s/extend", volID)
	} else {
		return fmt.Errorf("volume shrinking is not supported")
	}

	request := &VolumeResizeRequest{
		SizeInGigabytes: newSizeGigabytes,
	}

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodPost, c.makeURL(cPoolID, resourcePath), &request, &volume, true)
	if err != nil {
		Logc(ctx).WithFields(logFields).WithError(err).Error("Error resizing volume.")
		return err
	}

	Logc(ctx).WithFields(logFields).Debug("Volume resize request issued.")

	// Resize is asynchronous; the driver waits for completion via WaitForVolumeStatus.

	return nil
}

// DeleteVolume deletes a volume.
func (c Client) DeleteVolume(ctx context.Context, volume *Volume) error {
	logFields := LogFields{
		"API":    "OVH.DeleteVolume",
		"volume": volume.ID,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Deleting volume.")

	cPoolID, volID, err := ParseVolumeID(volume.ID)
	if err != nil {
		return err
	}

	resourcePath := fmt.Sprintf("/share/%s", volID)

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodDelete, c.makeURL(cPoolID, resourcePath), nil, nil, true)
	if err != nil {
		if IsOVHNotFoundError(err) {
			Logc(ctx).WithFields(logFields).Info("Volume already deleted.")
			return nil
		}

		Logc(ctx).WithFields(logFields).WithError(err).Error("Error deleting volume.")
		return err
	}

	Logc(ctx).WithFields(logFields).Debug("Volume deletion started.")

	return nil
}

func (c Client) VolumeAccessPaths(ctx context.Context, volume *Volume) ([]*AccessPath, error) {
	logFields := LogFields{
		"API":    "OVH.GetVolumeAccessPaths",
		"volume": volume.ID,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Fetching volume access paths.")

	cPoolID, volID, err := ParseVolumeID(volume.ID)
	if err != nil {
		return nil, err
	}

	resourcePath := fmt.Sprintf("/share/%s/accessPath", volID)

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	var aps []*AccessPath
	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodGet, c.makeURL(cPoolID, resourcePath), nil, &aps, true)
	if err != nil {
		return nil, err
	}

	Logc(ctx).WithFields(logFields).Debug("Found access paths.")

	return aps, nil
}

// ExportRulesForVolume returns a list of all export rules for a volume.
func (c Client) ExportRulesForVolume(ctx context.Context, volume *Volume) ([]*ExportRule, error) {
	logFields := LogFields{
		"API":    "OVH.ListExportRulesForVolume",
		"volume": volume.ID,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Fetching export rules for volume.")

	cPoolID, volID, err := ParseVolumeID(volume.ID)
	if err != nil {
		return nil, err
	}

	resourcePath := fmt.Sprintf("/share/%s/acl", volID)

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	var expRules []*ExportRule
	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodGet, c.makeURL(cPoolID, resourcePath), nil, &expRules, true)
	if err != nil {
		Logc(ctx).WithError(err).Debug("Failed to get export rules.")
		return nil, err
	}

	return expRules, nil
}

// ExportRuleByID returns an based on its ID.
func (c Client) ExportRuleByID(ctx context.Context, volume *Volume, exportRuleID string) (*ExportRule, error) {
	logFields := LogFields{
		"API":        "OVH.GetExportRule",
		"volume":     volume.ID,
		"exportRule": exportRuleID,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Fetching export rule by ID.")

	cPoolID, volID, err := ParseVolumeID(volume.ID)
	if err != nil {
		return nil, err
	}

	resourcePath := fmt.Sprintf("/share/%s/acl/%s", volID, exportRuleID)

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	var expRule ExportRule
	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodGet, c.makeURL(cPoolID, resourcePath), nil, &expRule, true)
	if err != nil {
		if IsOVHNotFoundError(err) {
			Logc(ctx).WithFields(logFields).Debug("Export rule not found.")
			return nil, errors.WrapWithNotFoundError(err, "export rule with ID '%s' not found", exportRuleID)
		}

		Logc(ctx).WithError(err).Debug("Failed to get export rule by ID.")
		return nil, err
	}

	Logc(ctx).WithFields(logFields).Debug("Found export rule by ID.")

	return &expRule, nil
}

// CreateExportRule creates a new export rule.
func (c Client) CreateExportRule(ctx context.Context, volume *Volume, request *ExportRuleCreateRequest) (*ExportRule, error) {
	logFields := LogFields{
		"API":         "OVH.CreateExportRule",
		"volume":      volume.ID,
		"accessLevel": request.AccessLevel,
		"accessTo":    request.AccessTo,
	}

	Logc(ctx).WithFields(LogFields{
		"volume":      volume.ID,
		"accessLevel": request.AccessLevel,
		"accessTo":    request.AccessTo,
	}).Debug("Issuing create request.")

	cPoolID, volID, err := ParseVolumeID(volume.ID)
	if err != nil {
		return nil, err
	}

	resourcePath := fmt.Sprintf("/share/%s/acl", volID)

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	var expr ExportRule
	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodPost, c.makeURL(cPoolID, resourcePath), request, &expr, true)
	if err != nil {
		Logc(ctx).WithFields(logFields).WithError(err).Error("Error creating export rule.")
		return nil, err
	}

	Logc(ctx).WithFields(logFields).Info("Export rule create request issued.")

	return &expr, nil
}

// WaitForExportRuleStatus watches for a desired export rule status and returns when that status is reached.
func (c Client) WaitForExportRuleStatus(ctx context.Context, volume *Volume, exportRule *ExportRule, desiredStatus string, abortStatuses []string,
	maxElapsedTime time.Duration,
) (string, error) {
	exportRuleStatus := ""

	checkExportRuleStatus := func() error {
		f, err := c.ExportRuleByID(ctx, volume, exportRule.ID)
		if err != nil {
			// Theres no 'denied' state in EFS -- export rule just vanishes. If we failed to query
			// the export rule info, and we're trying to transition to StatusDenied, and we get back a HTTP 404,
			// then return success. Otherwise, log the error as usual.
			// NB: Export Rules do NOT have to be deleted for/during volume deletion.
			if desiredStatus == ExportRuleStatusDenied && errors.IsNotFoundError(err) {
				Logc(ctx).Debugf("Implied deletion for export rule %s.", exportRule.ID)
				exportRuleStatus = ExportRuleStatusDenied
				return nil
			}

			if errors.Is(err, context.Canceled) {
				return backoff.Permanent(err)
			}

			exportRuleStatus = ""
			return fmt.Errorf("could not get export rule status; %v", err)
		}

		if f.Status == desiredStatus {
			Logc(ctx).WithFields(LogFields{
				"desiredStatus": desiredStatus,
				"volume":        volume.ID,
				"exportRule":    exportRule.ID,
			}).Debug("Desired export rule status reached.")
			return nil
		}

		err = fmt.Errorf("export rule status is %s, not %s", f.Status, desiredStatus)

		// Return a permanent error to stop retrying if we reached one of the abort states
		if collection.ContainsString(abortStatuses, f.Status) {
			return backoff.Permanent(TerminalState(err))
		}

		return err
	}

	stateNotify := func(err error, duration time.Duration) {
		Logc(ctx).WithFields(LogFields{
			"increment": duration,
			"message":   err.Error(),
		}).Debugf("Waiting for export rule status.")
	}
	stateBackoff := backoff.NewExponentialBackOff()
	stateBackoff.MaxElapsedTime = maxElapsedTime
	stateBackoff.MaxInterval = 5 * time.Second
	stateBackoff.RandomizationFactor = 0.1
	stateBackoff.InitialInterval = backoff.DefaultInitialInterval
	stateBackoff.Multiplier = 1.414

	Logc(ctx).WithField("desiredStatus", desiredStatus).Info("Waiting for export rule status.")

	if err := backoff.RetryNotify(checkExportRuleStatus, stateBackoff, stateNotify); err != nil {
		if IsTerminalStateError(err) {
			Logc(ctx).Errorf("Export rule reached terminal status.")
		} else {
			Logc(ctx).Errorf("Export rule status was not any of %s after %3.2f seconds.",
				[]string{desiredStatus}, stateBackoff.MaxElapsedTime.Seconds())
		}
		return exportRuleStatus, err
	}

	Logc(ctx).WithField("desiredStatus", []string{desiredStatus}).Debug("Desired export rule status reached.")

	return exportRuleStatus, nil
}

// DeleteExportRule deletes an export rule.
func (c Client) DeleteExportRule(ctx context.Context, volume *Volume, exportRule *ExportRule) error {
	logFields := LogFields{
		"API":        "OVH.DeleteExportRule",
		"volume":     volume.ID,
		"exportRule": exportRule.ID,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Deleting an export rule.")

	cPoolID, volID, err := ParseVolumeID(volume.ID)
	if err != nil {
		return err
	}

	resourcePath := fmt.Sprintf("/share/%s/acl/%s", volID, exportRule.ID)

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodDelete, c.makeURL(cPoolID, resourcePath), nil, nil, true)
	if err != nil {
		if IsOVHNotFoundError(err) {
			Logc(ctx).WithFields(logFields).Info("Export rule already deleted.")
			return nil
		}

		Logc(ctx).WithFields(logFields).WithError(err).Error("Error deleting export rule.")
		return err
	}

	Logc(ctx).WithFields(logFields).Debug("Export rule deleted.")

	return nil
}

// newSnapshotFromEFSSnapshot creates a new internal Snapshot struct from a EFSSnapshot.
func (c Client) newSnapshotFromEFSSnapshot(_ context.Context, snapshot *EFSSnapshot) (*Snapshot, error) {
	if snapshot == nil {
		return nil, errors.New("snapshot may not be nil")
	}

	capacityPool, _, _, err := ParseSnapshotID(snapshot.ID)
	if err != nil {
		return nil, err
	}

	return &Snapshot{
		ID:        snapshot.ID,
		CreatedAt: snapshot.CreatedAt,
		Name:      snapshot.Name,
		ServiceID: capacityPool,
		Path:      snapshot.Path,
		Status:    snapshot.Status,
	}, nil
}

// SnapshotsForVolume returns a list of snapshots of a volume.
func (c Client) SnapshotsForVolume(ctx context.Context, volume *Volume) (*[]*Snapshot, error) {
	logFields := LogFields{
		"API":    "OVH.ListSnapshotsForVolume",
		"volume": volume.ID,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Fetching snapshots for volume.")

	var snapshots []*Snapshot

	cPoolID, volID, err := ParseVolumeID(volume.ID)
	if err != nil {
		return nil, err
	}

	resourcePath := fmt.Sprintf("/share/%s/snapshot?detail=true", volID)

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	var efsSnapshots []*EFSSnapshot
	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodGet, c.makeURL(cPoolID, resourcePath), nil, &efsSnapshots, true)
	if err != nil {
		Logc(ctx).WithError(err).Error("Failed to get snapshots.")
		return nil, err
	}

	for _, efsSnapshot := range efsSnapshots {
		efsSnapshot.ID = CreateSnapshotID(cPoolID, volID, efsSnapshot.ID)
		snapshot, snapErr := c.newSnapshotFromEFSSnapshot(ctx, efsSnapshot)
		if snapErr != nil {
			Logc(ctx).WithError(snapErr).Error("Internal error creating snapshot.")
			return nil, snapErr
		}
		snapshots = append(snapshots, snapshot)
	}

	Logc(ctx).WithFields(logFields).Debug("Read snapshots from volume.")

	return &snapshots, nil
}

// SnapshotForVolume fetches a specific snapshot on a volume by its name.
func (c Client) SnapshotForVolume(ctx context.Context, volume *Volume, snapshotName string) (*Snapshot, error) {
	logFields := LogFields{
		"API":      "OVH.GetSnapshotForVolume",
		"volume":   volume.ID,
		"snapshot": snapshotName,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Fetching snapshot by name.")

	// TODO: (feat) filter snapshots by name
	// There's no endpoint to fetch snapshots by name for a given volume.
	// We fetch all snapshots for a volume then filter on name.
	snapshots, err := c.SnapshotsForVolume(ctx, volume)
	if err != nil {
		return nil, err
	}

	for _, snapshot := range *snapshots {
		if snapshot.Name == snapshotName {
			Logc(ctx).WithFields(logFields).Debug("Found snapshot.")
			return snapshot, nil
		}
	}

	Logc(ctx).WithFields(logFields).Debug("Snapshot not found.")
	return nil, errors.NotFoundError("snapshot '%s' not found", snapshotName)
}

// SnapshotByID fetches a specific snapshot on a volume by its ID.
func (c Client) SnapshotByID(ctx context.Context, volume *Volume, snapshotID string) (*Snapshot, error) {
	logFields := LogFields{
		"API":      "OVH.SnapshotByID",
		"volume":   volume.ID,
		"snapshot": snapshotID,
	}
	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Fetching snapshot by ID.")

	cPoolName, volID, snapID, err := ParseSnapshotID(snapshotID)
	if err != nil {
		return nil, err
	}

	resourcePath := fmt.Sprintf("/share/%s/snapshot/%s", volID, snapID)

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	var efsSnapshot EFSSnapshot
	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodGet, c.makeURL(cPoolName, resourcePath), nil, &efsSnapshot, true)
	if err != nil {
		if IsOVHNotFoundError(err) {
			Logc(ctx).WithFields(logFields).Debug("Snapshot not found.")
			return nil, errors.WrapWithNotFoundError(err, "snapshot '%s' not found", snapshotID)
		}

		Logc(ctx).WithError(err).Error("Failed to get snapshot.")
		return nil, err
	}

	Logc(ctx).WithFields(logFields).Debug("Found snapshot.")

	efsSnapshot.ID = snapshotID
	return c.newSnapshotFromEFSSnapshot(ctx, &efsSnapshot)
}

// WaitForSnapshotStatus waits for a desired snapshot state and returns once that state is achieved.
func (c Client) WaitForSnapshotStatus(ctx context.Context, volume *Volume, snapshot *Snapshot, desiredStatus string, abortStatuses []string, maxElapsedTime time.Duration,
) error {
	checkSnapshotStatus := func() error {
		s, err := c.SnapshotByID(ctx, volume, snapshot.ID)
		if err != nil {
			// There's no 'deleted' status in EFS -- snapshot just vanishes. If we failed to query
			// the export rule info, and we're trying to transition to StatusDeleted, and we got back a HTTP 404,
			// then return success. Otherwise, log the error as usual.
			// NOTE: Snapshots have to be deleted before their Volume is.
			if desiredStatus == SnapshotStatusDeleted && errors.IsNotFoundError(err) {
				Logc(ctx).Debugf("Implied deletion for snapshot '%s'.", snapshot.ID)
				return nil
			}

			if errors.Is(err, context.Canceled) {
				return backoff.Permanent(err)
			}
			return fmt.Errorf("could not get snapshot status; %v", err)
		}

		if s.Status == desiredStatus {
			Logc(ctx).WithFields(LogFields{
				"desiredStatus": desiredStatus,
				"snapshot":      snapshot.ID,
			}).Debug("Desired snapshot status reached.")
			return nil
		}

		err = fmt.Errorf("snapshot status is %s, not %s", s.Status, desiredStatus)

		// Return permanent error to stop retrying if we reached one of the abort states
		if collection.ContainsString(abortStatuses, s.Status) {
			return backoff.Permanent(TerminalState(err))
		}

		return err
	}

	stateNotify := func(err error, duration time.Duration) {
		Logc(ctx).WithFields(LogFields{
			"increment": duration.Truncate(10 * time.Millisecond),
			"message":   err.Error(),
		}).Debugf("Waiting for snapshot state.")
	}

	stateBackoff := backoff.NewExponentialBackOff()
	stateBackoff.MaxElapsedTime = maxElapsedTime
	stateBackoff.MaxInterval = 5 * time.Second
	stateBackoff.RandomizationFactor = 0.1
	stateBackoff.InitialInterval = backoff.DefaultInitialInterval
	stateBackoff.Multiplier = 1.414

	Logc(ctx).WithField("desiredStatus", desiredStatus).Info("Waiting for snapshot status.")

	if err := backoff.RetryNotify(checkSnapshotStatus, stateBackoff, stateNotify); err != nil {
		if IsTerminalStateError(err) {
			Logc(ctx).WithError(err).Error("Snapshot reached terminal status.")
		} else {
			Logc(ctx).Warningf("Snapshot status was not any of %s after %3.2f seconds.",
				[]string{desiredStatus}, stateBackoff.MaxElapsedTime.Seconds())
		}
		return err
	}

	Logc(ctx).WithField("desiredStatus", desiredStatus).Debugf("Desired snapshot status reached.")

	return nil
}

// CreateSnapshot creates a new snapshot.
func (c Client) CreateSnapshot(ctx context.Context, volume *Volume, name string) (*Snapshot, error) {
	logFields := LogFields{
		"API":      "OVH.CreateSnapshot",
		"volume":   volume.ID,
		"snapshot": name,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Creating snapshot.")

	cPoolID, volID, err := ParseVolumeID(volume.ID)
	if err != nil {
		return nil, err
	}

	resourcePath := fmt.Sprintf("/share/%s/snapshot", volID)

	req := &SnapshotCreateRequest{
		Name: name,
	}

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	var snapshot EFSSnapshot
	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodPost, c.makeURL(cPoolID, resourcePath), req, &snapshot, true)
	if err != nil {
		Logc(ctx).WithFields(logFields).WithError(err).Error("Error creating snapshot.")
		return nil, err
	}

	Logc(ctx).WithFields(LogFields{
		"volume":   volume.ID,
		"snapshot": snapshot.ID,
	}).Info("Snapshot create request issued.")

	// Forge snapshot ID from returned ID.
	newSnapID := CreateSnapshotID(cPoolID, volID, snapshot.ID)
	snapshot.ID = newSnapID

	return c.newSnapshotFromEFSSnapshot(ctx, &snapshot)
}

// RestoreSnapshot restores a volume to a snapshot.
// NOTE: EFS offer supports volume restoration using the latest snapshot only.
func (c Client) RestoreSnapshot(ctx context.Context, volume *Volume, snapshot *Snapshot) error {
	logFields := LogFields{
		"API":      "OVH.RestoreSnapshot",
		"volume":   volume.ID,
		"snapshot": snapshot.ID,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Restoring snapshot.")

	cPoolID, volID, err := ParseVolumeID(volume.ID)
	if err != nil {
		return err
	}

	_, _, snapID, err := ParseSnapshotID(snapshot.ID)
	if err != nil {
		return err
	}

	snapshotRevertRequest := &SnapshotRevertRequest{
		SnapshotID: snapID,
	}

	resourcePath := fmt.Sprintf("/share/%s/revert", volID)

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodPost, c.makeURL(cPoolID, resourcePath), snapshotRevertRequest, nil, true)
	if err != nil {
		return err
	}

	Logc(ctx).WithFields(logFields).Debug("Volume restoration to snapshot started.")

	return nil
}

// DeleteSnapshot deletes a snapshot.
func (c Client) DeleteSnapshot(ctx context.Context, volume *Volume, snapshot *Snapshot) error {
	logFields := LogFields{
		"API":      "OVH.DeleteSnapshot",
		"volume":   volume.ID,
		"snapshot": snapshot.ID,
	}

	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
		WithFields(logFields).Trace("Deleting snapshot.")

	cPoolID, volID, err := ParseVolumeID(volume.ID)
	if err != nil {
		return err
	}

	_, _, snapID, err := ParseSnapshotID(snapshot.ID)

	resourcePath := fmt.Sprintf("/share/%s/snapshot/%s", volID, snapID)

	apiCtx, apiCancel := context.WithTimeout(ctx, c.config.APITimeout)
	defer apiCancel()

	err = c.sdkClient.httpClient.CallAPIWithContext(apiCtx, http.MethodDelete, c.makeURL(cPoolID, resourcePath), nil, nil, true)
	if err != nil {
		if IsOVHNotFoundError(err) {
			Logc(ctx).WithFields(logFields).Info("Snapshot already deleted.")
			return nil
		}

		Logc(ctx).WithFields(logFields).WithError(err).Error("Error deleting snapshot.")
		return err
	}

	Logc(ctx).WithFields(logFields).Debug("Snapshot deletion started.")

	return nil
}

// TerminalStateError signals that the object is in a terminal state.  This is used to stop waiting on
// an object to change state.
type TerminalStateError struct {
	Err error
}

func (e *TerminalStateError) Error() string {
	return e.Err.Error()
}

// TerminalState wraps the given err in a *TerminalStateError.
func TerminalState(err error) *TerminalStateError {
	return &TerminalStateError{
		Err: err,
	}
}

func IsTerminalStateError(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*TerminalStateError)
	return ok
}
