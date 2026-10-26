package ondc.policy

import rego.v1

# Shim 1 network policy for ONDC retail requests that reach the seller app.
#
# This checks the parts of the request that need no state and no clock:
# mandatory context fields, "is this request meant for us", and "do we serve this
# domain, city, country and version". The time-based and registry-based checks
# are in the ondcguard step.
#
# Settings come from the checkPolicy plugin config in the adapter YAML:
#   data.config.ownSubscriberId   our subscriber id (bpp_id must match)
#   data.config.ownUri            our subscriber URL (bpp_uri must match)
#   data.config.allowedDomains    comma separated, e.g. "ONDC:RET10"
#   data.config.allowedCities     comma separated, e.g. "std:080,std:011"
#   data.config.allowedCountries  comma separated, e.g. "IND"
#   data.config.allowedVersions   comma separated, e.g. "1.2.0"
#
# Every violation uses ONDC error code 30000 (invalid request).

default result := {
	"valid": true,
	"violations": [],
}

result := {
	"valid": count(violations) == 0,
	"violations": violations,
}

# Fields every ONDC request must carry.
common_fields := [
	"domain", "country", "city", "action", "core_version",
	"bap_id", "bap_uri", "transaction_id", "message_id", "timestamp", "ttl",
]

# Fields that only a request sent straight to one seller carries. A search that
# comes through the Gateway is broadcast, so it has no bpp_id or bpp_uri.
direct_fields := ["bpp_id", "bpp_uri"]

field_present(name) if {
	is_string(input.context[name])
	input.context[name] != ""
}

violation(message) := {"code": "30000", "message": message}

violations contains violation(sprintf("context.%s is required", [name])) if {
	some name in common_fields
	not field_present(name)
}

violations contains violation(sprintf("context.%s is required for %s", [name, input.context.action])) if {
	input.context.action != "search"
	some name in direct_fields
	not field_present(name)
}

violations contains violation("context.bpp_id is not this seller") if {
	input.context.action != "search"
	field_present("bpp_id")
	input.context.bpp_id != data.config.ownSubscriberId
}

violations contains violation("context.bpp_uri is not this seller") if {
	input.context.action != "search"
	field_present("bpp_uri")
	input.context.bpp_uri != data.config.ownUri
}

violations contains violation(sprintf("domain %s is not served here", [input.context.domain])) if {
	field_present("domain")
	not input.context.domain in split(data.config.allowedDomains, ",")
}

violations contains violation(sprintf("city %s is not served here", [input.context.city])) if {
	field_present("city")
	not input.context.city in split(data.config.allowedCities, ",")
}

violations contains violation(sprintf("country %s is not served here", [input.context.country])) if {
	field_present("country")
	not input.context.country in split(data.config.allowedCountries, ",")
}

violations contains violation(sprintf("core_version %s is not supported", [input.context.core_version])) if {
	field_present("core_version")
	not input.context.core_version in split(data.config.allowedVersions, ",")
}
