package api

import "github.com/ovh/go-ovh/ovh"

// IsOVHNotFoundError checks whether an error is an ovh.APIError with "Client::NotFound" class.
// Log example:
// time="2024-07-17T09:58:30Z" level=trace msg=IsOVHNotFoundError.
// API=OVH.GetVolumeByID
// errType="*ovh.APIError"
// error="OVHcloud API error (status code 404): Client::NotFound: \"Resource not found\" (X-OVH-Query-Id: EU.ext-3.669795c6.3806160.fcc36c62d60ff434766fbaca125e36cb)"
// requestID=b340c7ef-f095-4d62-95ed-ca2de2c09f43
// requestSource=CSI
// volume=c4c5c704-0c2c-4bfe-bdc2-5f71d3625de5
func IsOVHNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	if notFoundErr, ok := err.(*ovh.APIError); ok {
		return notFoundErr.Class == "Client::NotFound"
	}

	return false
}
