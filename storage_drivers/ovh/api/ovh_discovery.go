package api

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"go.uber.org/multierr"

	"github.com/cenkalti/backoff/v4"

	. "github.com/netapp/trident/logging"
	"github.com/netapp/trident/pkg/collection"
	"github.com/netapp/trident/pkg/convert"
	"github.com/netapp/trident/storage"
	"github.com/netapp/trident/utils/errors"
)

const (
	serviceLevel       = "serviceLevel"
	capacityPools      = "capacityPools"
	DefaultMaxCacheAge = 10 * time.Minute
)

// ///////////////////////////////////////////////////////////////////////////////
// Top level discovery functions
// ///////////////////////////////////////////////////////////////////////////////

// RefreshOVHResources refreshes the cache of discovered OVH resources and validates
// them against our known storage pools.
func (c Client) RefreshOVHResources(ctx context.Context) error {
	if time.Now().Before(c.sdkClient.OVHResources.lastUpdateTime.Add(c.config.MaxCacheAge)) {
		Logc(ctx).Debugf("Cached resources not yet %v old, skipping refresh.", c.config.MaxCacheAge)
		return nil
	}

	// (re-)Discover OVH resources
	Logc(ctx).Debugf("Discovering OVH resources.")
	discoveryErr := multierr.Combine(c.DiscoverOVHResources(ctx))

	// This is noisy, hide it behind api tracing.
	c.dumpOVHResources(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"])

	// TODO: Warn about anything in the config that doesn't match any discovered resources
	c.checkForNonexistentCapacityPools(ctx)

	// Return errors for any storage pools that cannot be satisfied by discovered resources
	poolErrors := multierr.Combine(c.checkForUnsatisfiedPools(ctx)...)
	discoveryErr = multierr.Combine(discoveryErr, poolErrors)

	return discoveryErr
}

// DiscoverOVHResources rediscovers the OVH resources we care about and updates the cache.
func (c Client) DiscoverOVHResources(ctx context.Context) (returnError error) {
	// Start from scratch each time we're called.
	newCapacityPoolsMap := make(map[string]*CapacityPool)

	defer func() {
		if returnError != nil {
			Logc(ctx).WithError(returnError).Debug("Discovery error, not retaining any discovered resources.i")
			return
		}

		// Swap the newly discovered resources into the cache only if discovery succeeded.
		c.sdkClient.OVHResources.CapacityPoolMap = newCapacityPoolsMap
		c.sdkClient.OVHResources.lastUpdateTime = time.Now()

		Logc(ctx).Debug("Switched to newly discovered resources.")
	}()

	// Discover capacity pools
	cPools, returnErr := c.discoverCapacityPoolsWithRetry(ctx)
	if returnErr != nil {
		return
	}

	// Update maps with all data form discovered capacity pools
	for _, cPool := range *cPools {
		newCapacityPoolsMap[cPool.Name] = cPool
	}

	// Detect the lack of any resources: can occur when no connectivity, etc.
	// Would like a better way of proactively finding out there is something wrong
	// at a very basic level.  (Reproduce this by turning off your network!)
	numCapacityPools := len(newCapacityPoolsMap)

	if numCapacityPools == 0 {
		return errors.New("no EFS storage pools discovered; volume provisioning may fail until corrected")
	}

	Logc(ctx).WithFields(LogFields{
		"capacityPools": numCapacityPools,
	}).Info("Discoverd EFS resources.")

	return
}

// dumpOVHResources writes a hierarchical representation of discovered resources to the log.
func (c Client) dumpOVHResources(ctx context.Context, driverName string, discoveryTraceEnabled bool) {
	Logd(ctx, driverName, discoveryTraceEnabled).Tracef("Dsicovered OVH Resources:")

	for _, cp := range c.sdkClient.OVHResources.CapacityPoolMap {
		Logd(ctx, driverName, discoveryTraceEnabled).Tracef("CPool: %s, [%s, %s]",
			cp.ID, cp.Name, cp.ServiceLevel)
	}
}

// checkForUnsatisfiedPools return one or more errors if one or more configured storage pools
// are satisfied by no capacity pools.
func (c Client) checkForUnsatisfiedPools(ctx context.Context) (discoveryErrors []error) {
	// Ensure every storage pool matches one or more capacity pools
	for sPoolName, sPool := range c.sdkClient.OVHResources.StoragePoolMap {
		// Find all capacity pools that work for this storage pool
		cPools := c.CapacityPoolsForStoragePool(ctx, sPool, sPool.InternalAttributes()[serviceLevel])

		if len(cPools) == 0 {
			err := fmt.Errorf("no capacity pools found for storage pool %s", sPoolName)
			Logc(ctx).WithError(err).Error("discovery error.")
			discoveryErrors = append(discoveryErrors, err)
		} else {
			cPoolNames := make([]string, 0)
			for _, cPool := range cPools {
				cPoolNames = append(cPoolNames, cPool.Name)
			}

			// Print the mapping in the logs so we see it after each discovery refresh.
			Logc(ctx).Debug("Storage pool %s mapped to capacity pool %v.", sPoolName, cPoolNames)
		}
	}

	return
}

// checkForNonexistentCapacityPools logs warning if any configured capacity pools do not
// match discovered capacity pools in the resource cache.
func (c Client) checkForNonexistentCapacityPools(ctx context.Context) (anyMismatches bool) {
	for sPoolName, sPool := range c.sdkClient.OVHResources.StoragePoolMap {
		// Build list of short and long capacity pool names
		cpNames := make([]string, 0)
		for _, cacheCP := range c.sdkClient.OVHResources.CapacityPoolMap {
			cpNames = append(cpNames, cacheCP.Name)
		}

		// Find any capacity pools value in this storage pool that doesn't match known capacity pools
		for _, configCP := range collection.SplitString(ctx, sPool.InternalAttributes()[capacityPools], ",") {
			if !collection.StringInSlice(configCP, cpNames) {
				anyMismatches = true

				Logc(ctx).WithFields(LogFields{
					"pool":         sPoolName,
					"capacityPool": configCP,
				}).Warning("Capacity pool referenced in pool not found.")
			}
		}
	}

	return
}

// ///////////////////////////////////////////////////////////////////////////////
// Internal functions to do discovery via the OVH SDK
// ///////////////////////////////////////////////////////////////////////////////

// discoverCapacityPoolsWithRetry queries OVH SDK for all capacity pools in the current location,
// retrying if the API request is throttled.
func (c Client) discoverCapacityPoolsWithRetry(ctx context.Context) (pools *[]*CapacityPool, err error) {
	discover := func() error {
		if pools, err = c.discoverCapacityPools(ctx); err != nil {
			return err
		}
		return backoff.Permanent(err)
	}

	notify := func(err error, duration time.Duration) {
		Logc(ctx).WithFields(LogFields{
			"increment": duration.Truncate(10 * time.Millisecond),
		}).Debugf("Retrying capacity pools query.")
	}

	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.MaxElapsedTime = DefaultTimeout
	expBackoff.MaxInterval = 5 * time.Second
	expBackoff.RandomizationFactor = 0.1
	expBackoff.InitialInterval = 5 * time.Second
	expBackoff.Multiplier = 1

	err = backoff.RetryNotify(discover, expBackoff, notify)

	return
}

// discoverCapacityPools queries OVH API for all EFS capacity pools in the current location.
func (c Client) discoverCapacityPools(ctx context.Context) (*[]*CapacityPool, error) {
	logFields := LogFields{
		"API": "OVHEFS.discoverCapacityPools",
	}

	var locations []string
	locations = append(locations, c.config.Location)

	var pools []*CapacityPool

	var capacityPools []*CapacityPool
	err := c.sdkClient.httpClient.CallAPIWithContext(ctx, http.MethodGet, "/storage/netapp", nil, &capacityPools, true)
	if err != nil {
		Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
			WithFields(logFields).WithError(err).Error("Could not list storage pools.")
		return nil, err

	}
	/*
		data, err := c.ListStoragePools(ctx)
		if err != nil {
			Logc(ctx).WithFields(logFields).WithError(err).Error("Capacity pool query failed.")
			return nil, err
		}
	*/
	for _, rawPool := range capacityPools {
		/*
			err := parseCapacityPoolID(rawPool.ID)
			if err != nil {
				Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["api"]).
					WithFields(logFields).WithError(err).Warning("Skipping pool.")
			}
		*/

		for _, location := range locations {
			if rawPool.Region == location {
				pools = append(pools, &CapacityPool{
					ID:           rawPool.ID,
					Name:         rawPool.ID,
					Region:       rawPool.Region,
					ServiceLevel: convert.ToTitle(rawPool.ServiceLevel),
					Status:       rawPool.Status,
				})
			}
		}
	}

	if len(pools) == 0 {
		return nil, errors.New("no capacity pools found for the given region")
	}
	return &pools, nil
}

/*
func (c Client) ListStoragePools(ctx context.Context) ([]*CapacityPool, error) {
	logFields := LogFields{
		"API": "OVHEFS.ListStoragePools",
	}

	return capacityPools, nil
        }
*/

// ///////////////////////////////////////////////////////////////////////////////
// API functions to match/search capacity pools
// ///////////////////////////////////////////////////////////////////////////////

// CapacityPools returns a list of all discovered OVH capacity pools.
func (c Client) CapacityPools() *[]*CapacityPool {
	var cPools []*CapacityPool

	for _, cPool := range c.sdkClient.OVHResources.CapacityPoolMap {
		cPools = append(cPools, cPool)
	}

	return &cPools
}

// capacityPool returns a single discovered capacity pool by its full name.
func (c Client) capacityPool(cPoolFullName string) *CapacityPool {
	return c.sdkClient.OVHResources.CapacityPoolMap[cPoolFullName]
}

// CapacityPoolsForStoragePools returns all discovered capacity pools matching all known storage pools,
// regardless of service levels.
func (c Client) CapacityPoolsForStoragePools(ctx context.Context) []*CapacityPool {
	// This map deduplicates cPools from multiple storage pools
	cPoolMap := make(map[*CapacityPool]bool)

	// Build deduplicated map of cPools
	for _, sPool := range c.sdkClient.OVHResources.StoragePoolMap {
		for _, cPool := range c.CapacityPoolsForStoragePool(ctx, sPool, "") {
			cPoolMap[cPool] = true
		}
	}

	// Copy keys into a list of deduplicated cPools
	cPools := make([]*CapacityPool, 0)

	for cPool := range cPoolMap {
		cPools = append(cPools, cPool)
	}

	return cPools
}

// CapacityPoolsForStoragePool returns all discovered capacity pools matching the specified
// storage pool and service level. The pools are shuffled to enable easier random selection.
func (c Client) CapacityPoolsForStoragePool(
	ctx context.Context, sPool storage.Pool, serviceLevel string,
) []*CapacityPool {
	Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["discovery"]).WithField("storagePool", sPool.Name()).
		Tracef("Determining capacity pools for storage pool.")

	// This map tracks which capacity pools have passed the filters
	filteredCapacityPoolMap := make(map[string]bool)

	// Start with all capacity pools marked as passing the filters
	for cPoolFullName := range c.sdkClient.OVHResources.CapacityPoolMap {
		filteredCapacityPoolMap[cPoolFullName] = true
	}

	// If capacity pools were specified, filter out non-matching capacity pools
	cpList := collection.SplitString(ctx, sPool.InternalAttributes()[capacityPools], ",")
	if len(cpList) > 0 {
		for cPoolFullName, cPool := range c.sdkClient.OVHResources.CapacityPoolMap {
			if !collection.ContainsString(cpList, cPool.Name) && !collection.ContainsString(cpList, cPoolFullName) {
				Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["discovery"]).Tracef("Ignoring capacity pool %s, not in capacity pools [%s].",
					cPoolFullName, cpList)
				filteredCapacityPoolMap[cPoolFullName] = false
			}
		}
	}

	// Filter out pools with non-matching service levels
	if serviceLevel != "" {
		for cPoolFullName, cPool := range c.sdkClient.OVHResources.CapacityPoolMap {
			if cPool.ServiceLevel != serviceLevel {
				Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["discovery"]).Tracef("Ignoring capacity pool %s, not service level %s.",
					cPoolFullName, serviceLevel)
				filteredCapacityPoolMap[cPoolFullName] = false
			}
		}
	}

	// Build list of all capacity pools that have passed all filters
	cPools := make([]*CapacityPool, 0)
	for cPoolFullName, match := range filteredCapacityPoolMap {
		if match {
			cPools = append(cPools, c.sdkClient.OVHResources.CapacityPoolMap[cPoolFullName])
		}
	}

	// Shuffle the pools
	rand.Shuffle(len(cPools), func(i, j int) { cPools[i], cPools[j] = cPools[j], cPools[i] })

	return cPools
}

// EnsureVolumeInValidCapacityPool checks whether the specified volume exists in any capacity pool that is
// referenced by the backend config. It returns nil if so, or if no capacity pools are named in the config.
func (c Client) EnsureVolumeInValidCapacityPool(ctx context.Context, volume *Volume) error {
	// Get a list of all capcait pools referenced in the config
	allCapacityPools := c.CapacityPoolsForStoragePools(ctx)

	// If we aren't restricting capacity pools, any capacity pool is OK
	if len(allCapacityPools) == 0 {
		return nil
	}

	for _, cPool := range allCapacityPools {
		if volume.ServiceID == cPool.ID {
			return nil
		}
	}

	return errors.NotFoundError("volume %s is part of another capacity pool not referenced "+
		"by this backend", volume.CreationToken)
}
