// Package searchgateway implements an opt-in Responses hosted-search adapter.
// It has no account scheduler and does not import the Balancer server.
package searchgateway

import (
 "encoding/json"
 "fmt"
 "io"
 "net/url"
 "regexp"
 "strings"
)

type Config struct {
 Listen string `json:"listen"`
 RequestTimeoutSec int `json:"requestTimeoutSec"`
 MaxConcurrent int `json:"maxConcurrent"`
 MaxSearches int `json:"maxSearches"`
 MaxBodyBytes int64 `json:"maxBodyBytes"`
 MaxResponseBytes int64 `json:"maxResponseBytes"`
 HistoryTTLSeconds int `json:"historyTtlSec"`
 HistoryMaxBytes int `json:"historyMaxBytes"`
 HistoryMaxEntries int `json:"historyMaxEntries"`
 Search SearchConfig `json:"search"`
 Routes []Route `json:"routes"`
}
type SearchConfig struct {
 Endpoint string `json:"endpoint"`
 APIKeyEnv string `json:"apiKeyEnv"`
 TimeoutSec int `json:"timeoutSec"`
 MaxResults int `json:"maxResults"`
 key string
}
type Route struct {
 ID string `json:"id"`
 TokenEnv string `json:"tokenEnv"`
 UpstreamBaseURL string `json:"upstreamBaseUrl"`
 UpstreamKeyEnv string `json:"upstreamKeyEnv"`
 Mode string `json:"mode"` // native | compat | disabled
 AllowedModels []string `json:"allowedModels,omitempty"`
 token, upstreamKey string
}

func ReadConfig(r io.Reader, getenv func(string)string) (Config,error) {
 var c Config
 d:=json.NewDecoder(io.LimitReader(r,1<<20));d.DisallowUnknownFields()
 if d.Decode(&c)!=nil {return c,fmt.Errorf("invalid gateway configuration JSON")}
 var extra any;if d.Decode(&extra)!=io.EOF {return c,fmt.Errorf("configuration must contain one JSON object")}
 return c,c.resolve(getenv)
}
func (c *Config) resolve(getenv func(string)string) error {
 if c.Listen=="" {c.Listen="127.0.0.1:8791"}
 defaults:=[]struct{p *int; def,max int}{
  {&c.RequestTimeoutSec,180,900},{&c.MaxConcurrent,16,256},{&c.MaxSearches,3,10},
  {&c.HistoryTTLSeconds,1800,86400},{&c.HistoryMaxBytes,32<<20,256<<20},{&c.HistoryMaxEntries,1024,10000},
  {&c.Search.TimeoutSec,20,120},{&c.Search.MaxResults,5,10},
 }
 for _,x:=range defaults {if *x.p==0 {*x.p=x.def};if *x.p<1 || *x.p>x.max {return fmt.Errorf("gateway limit outside supported range")}}
 if c.MaxBodyBytes==0 {c.MaxBodyBytes=4<<20};if c.MaxResponseBytes==0 {c.MaxResponseBytes=8<<20}
 if c.MaxBodyBytes<1024 || c.MaxBodyBytes>100<<20 || c.MaxResponseBytes<1024 || c.MaxResponseBytes>32<<20 {return fmt.Errorf("gateway byte limit outside supported range")}
 if len(c.Routes)==0 || len(c.Routes)>128 {return fmt.Errorf("configure 1–128 routes")}
 ids:=map[string]bool{};needSearch:=false
 for i:=range c.Routes {
  r:=&c.Routes[i]
  if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`).MatchString(r.ID) || ids[r.ID] {return fmt.Errorf("invalid or duplicate route id")};ids[r.ID]=true
  if !validBase(r.UpstreamBaseURL,false) || !strings.HasSuffix(r.UpstreamBaseURL,"/v1") {return fmt.Errorf("route %s requires a credential-free /v1 base",r.ID)}
  if r.Mode!="native" && r.Mode!="compat" && r.Mode!="disabled" {return fmt.Errorf("route %s requires explicit native/compat/disabled mode",r.ID)}
  if r.Mode=="compat" {needSearch=true;if len(r.AllowedModels)==0 {return fmt.Errorf("compat route %s requires allowedModels",r.ID)}}
  for _,m:=range r.AllowedModels {if strings.TrimSpace(m)=="" || len(m)>256 {return fmt.Errorf("invalid allowed model")}}
  if !validEnv(r.TokenEnv) || !validEnv(r.UpstreamKeyEnv) {return fmt.Errorf("route %s requires credential environment variable names",r.ID)}
  r.token=getenv(r.TokenEnv);r.upstreamKey=getenv(r.UpstreamKeyEnv)
  if !validSecret(r.token) || !validSecret(r.upstreamKey) {return fmt.Errorf("missing or invalid credentials for route %s",r.ID)}
 }
 if needSearch {
  if !validBase(c.Search.Endpoint,true) || !validEnv(c.Search.APIKeyEnv) {return fmt.Errorf("search requires an HTTPS endpoint and apiKeyEnv")}
  c.Search.key=getenv(c.Search.APIKeyEnv);if !validSecret(c.Search.key){return fmt.Errorf("missing or invalid search credential")}
 }
 return nil
}
func validBase(s string,httpsOnly bool) bool {u,e:=url.Parse(s);return e==nil && u.Host!="" && (u.Scheme=="https" || !httpsOnly && u.Scheme=="http") && u.User==nil && u.RawQuery=="" && !u.ForceQuery && u.Fragment=="" && !strings.HasSuffix(s,"/") && !strings.Contains(u.Path,"..")}
func validEnv(s string)bool{return regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(s)}
func validSecret(s string)bool{return len(s)>=16 && len(s)<=4096 && !strings.ContainsAny(s,"\r\n\x00") && strings.TrimSpace(s)==s}
