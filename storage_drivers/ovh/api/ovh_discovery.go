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
	stale, resourceUpdater, unlocker := c.sdkClient.resources.LockAndCheckStale(c.config.MaxCacheAge)

	if !stale {
		Logc(ctx).Debugf("Cached resources not yet %v old, skipping refresh.", c.config.MaxCacheAge)
		unlocker()
		return nil
	}

	// (re-)Discover OVH resources
	Logc(ctx).Debugf("Discovering OVH resources.")
	discoveryErr := multierr.Combine(c.discoverOVHResources(ctx, resourceUpdater))
	// Concurrent operations may continue after this point even if discovery failed
	unlocker()

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
func (c Client) DiscoverOVHResources(ctx context.Context) error {
	resourceUpdater, unlocker := c.sdkClient.resources.LockForDiscover()
	defer unlocker()
	return c.discoverOVHResources(ctx, resourceUpdater)
}

// discoverOVHResources rediscovers OVH resources and updates the cache via resourceUpdater.
// Caller must hold the client's resources lock for the duration of this call.
func (c Client) discoverOVHResources(ctx context.Context, resourceUpdater ovhResourceUpdater) (returnError error) {
	// Start from scratch each time we're called.
	newCapacityPoolsMap := make(map[string]*CapacityPool)

	defer func() {
		if returnError != nil {
			Logc(ctx).WithError(returnError).Debug("Discovery error, not retaining any discovered resources.")
			return
		}

		// Swap the newly discovered resources into the cache only if discovery succeeded.
		resourceUpdater(time.Now(), newCapacityPoolsMap)

		Logc(ctx).Debug("Switched to newly discovered resources.")
	}()

	// Discover capacity pools
	cPools, returnError := c.discoverCapacityPoolsWithRetry(ctx)
	if returnError != nil {
		return
	}

	// Update maps with all data from discovered capacity pools
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
	}).Info("Discovered EFS resources.")

	return
}

// dumpOVHResources writes a hierarchical representation of discovered resources to the log.
func (c Client) dumpOVHResources(ctx context.Context, driverName string, discoveryTraceEnabled bool) {
	Logd(ctx, driverName, discoveryTraceEnabled).Tracef("Discovered OVH Resources:")

	c.sdkClient.resources.GetCapacityPools().Range(func(_ string, cp *CapacityPool) bool {
		Logd(ctx, driverName, discoveryTraceEnabled).Tracef("CPool: %s, [%s, %s]",
			cp.ID, cp.Name, cp.ServiceLevel)
		return true
	})
}

// checkForUnsatisfiedPools return one or more errors if one or more configured storage pools
// are satisfied by no capacity pools.
func (c Client) checkForUnsatisfiedPools(ctx context.Context) (discoveryErrors []error) {
	// Ensure every storage pool matches one or more capacity pools
	c.sdkClient.resources.GetStoragePools().Range(func(sPoolName string, sPool storage.Pool) bool {
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
			Logc(ctx).Debugf("Storage pool %s mapped to capacity pool %v.", sPoolName, cPoolNames)
		}
		return true
	})

	return
}

// checkForNonexistentCapacityPools logs warning if any configured capacity pools do not
// match discovered capacity pools in the resource cache.
func (c Client) checkForNonexistentCapacityPools(ctx context.Context) (anyMismatches bool) {
	// Build list of short and long capacity pool names
	cpNames := make([]string, 0)
	c.sdkClient.resources.GetCapacityPools().Range(func(_ string, cacheCP *CapacityPool) bool {
		cpNames = append(cpNames, cacheCP.Name)
		return true
	})

	c.sdkClient.resources.GetStoragePools().Range(func(sPoolName string, sPool storage.Pool) bool {

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
		return true
	})

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

	c.sdkClient.resources.GetCapacityPools().Range(func(_ string, cPool *CapacityPool) bool {
		cPools = append(cPools, cPool)
		return true
	})

	return &cPools
}

// capacityPool returns a single discovered capacity pool by its full name.
func (c Client) capacityPool(cPoolFullName string) *CapacityPool {
	return c.sdkClient.resources.GetCapacityPools().Get(cPoolFullName)
}

// CapacityPoolsForStoragePools returns all discovered capacity pools matching all known storage pools,
// regardless of service levels.
func (c Client) CapacityPoolsForStoragePools(ctx context.Context) []*CapacityPool {
	// This map deduplicates cPools from multiple storage pools
	cPoolMap := make(map[*CapacityPool]bool)

	// Build deduplicated map of cPools
	c.sdkClient.resources.GetStoragePools().Range(func(_ string, sPool storage.Pool) bool {
		for _, cPool := range c.CapacityPoolsForStoragePool(ctx, sPool, "") {
			cPoolMap[cPool] = true
		}
		return true
	})

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

	// Take a consistent view of the cache, as it may be swapped by a concurrent refresh
	capacityPoolMap := c.sdkClient.resources.GetCapacityPools()

	// This map tracks which capacity pools have passed the filters
	filteredCapacityPoolMap := make(map[string]bool)

	// Start with all capacity pools marked as passing the filters
	capacityPoolMap.Range(func(cPoolFullName string, _ *CapacityPool) bool {
		filteredCapacityPoolMap[cPoolFullName] = true
		return true
	})

	// If capacity pools were specified, filter out non-matching capacity pools
	cpList := collection.SplitString(ctx, sPool.InternalAttributes()[capacityPools], ",")
	if len(cpList) > 0 {
		capacityPoolMap.Range(func(cPoolFullName string, cPool *CapacityPool) bool {
			if !collection.ContainsString(cpList, cPool.Name) && !collection.ContainsString(cpList, cPoolFullName) {
				Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["discovery"]).Tracef("Ignoring capacity pool %s, not in capacity pools [%s].",
					cPoolFullName, cpList)
				filteredCapacityPoolMap[cPoolFullName] = false
			}
			return true
		})
	}

	// Filter out pools with non-matching service levels
	if serviceLevel != "" {
		capacityPoolMap.Range(func(cPoolFullName string, cPool *CapacityPool) bool {
			if cPool.ServiceLevel != serviceLevel {
				Logd(ctx, c.config.StorageDriverName, c.config.DebugTraceFlags["discovery"]).Tracef("Ignoring capacity pool %s, not service level %s.",
					cPoolFullName, serviceLevel)
				filteredCapacityPoolMap[cPoolFullName] = false
			}
			return true
		})
	}

	// Build list of all capacity pools that have passed all filters
	cPools := make([]*CapacityPool, 0)
	for cPoolFullName, match := range filteredCapacityPoolMap {
		if match {
			cPools = append(cPools, capacityPoolMap.Get(cPoolFullName))
		}
	}

	// Shuffle the pools
	rand.Shuffle(len(cPools), func(i, j int) { cPools[i], cPools[j] = cPools[j], cPools[i] })

	return cPools
}

// EnsureVolumeInValidCapacityPool checks whether the specified volume exists in any capacity pool that is
// referenced by the backend config. It returns nil if so, or if no capacity pools are named in the config.
func (c Client) EnsureVolumeInValidCapacityPool(ctx context.Context, volume *Volume) error {
	// Get a list of all capacity pools referenced in the config
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
