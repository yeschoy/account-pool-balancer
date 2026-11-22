package searchgateway

import (
 "bytes"
 "encoding/json"

 "account-pool-balancer/internal/websearch"
)

type searchOptions struct { domains []string; contextSize string }

// Convert only documented, representable options. In particular a cache-only
// request must never silently become a live third-party fetch.
func parseSearchOptions(t object)(searchOptions,error) {
 var out searchOptions
 for key,raw:=range t {
  switch key {
  case "type":
  case "external_web_access":
   var live bool
   if bytes.Equal(raw,[]byte("null")) || json.Unmarshal(raw,&live)!=nil{return out,invalid("invalid_search_options","external_web_access must be boolean")}
   if !live{return out,invalid("unsupported_cached_search","This backend has no verified cache-only guarantee; no live search was sent")}
  case "indexed_web_access":
   var indexed bool
   if bytes.Equal(raw,[]byte("null")) || json.Unmarshal(raw,&indexed)!=nil{return out,invalid("invalid_search_options","indexed_web_access must be boolean")}
   if indexed{return out,invalid("unsupported_indexed_search","This backend cannot guarantee index-restricted page access")}
  case "search_context_size":
   out.contextSize=str(raw)
   if out.contextSize!="low" && out.contextSize!="medium" && out.contextSize!="high" {return out,invalid("invalid_search_options","search_context_size must be low, medium or high")}
  case "search_content_types":
   var types []string
   if json.Unmarshal(raw,&types)!=nil || len(types)!=1 || types[0]!="text" {return out,invalid("unsupported_search_content","Only text search results are implemented")}
  case "filters":
   if bytes.Equal(raw,[]byte("null")){continue}
   filters,e:=decodeObject(raw);if e!=nil{return out,invalid("invalid_search_filters","filters must be an object or null")}
   for k,v:=range filters {
    if k!="allowed_domains" {return out,invalid("unsupported_search_filters","Only allowed_domains filtering is implemented")}
    var domains []string
    if json.Unmarshal(v,&domains)!=nil || bytes.Equal(v,[]byte("null")) {return out,invalid("invalid_search_filters","allowed_domains must be an array of domains")}
    out.domains,e=websearch.NormalizeDomains(domains)
    if e!=nil{return out,invalid("invalid_search_filters","Provide up to 100 plain DNS domains without URLs, wildcards or IP addresses")}
   }
  case "user_location":
   if bytes.Equal(raw,[]byte("null")){continue}
   location,e:=decodeObject(raw)
   if e!=nil || len(location)!=1 || str(location["type"])!="approximate" {return out,invalid("unsupported_search_location","Geographic targeting is not implemented; no location fields were discarded")}
  default:return out,invalid("unsupported_search_options","An unimplemented web search option was supplied; no options were discarded")
  }
 }
 return out,nil
}
