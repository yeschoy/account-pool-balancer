// Package websearch executes bounded searches, independently of model protocols.
package websearch

import (
 "context"
 "encoding/json"
 "errors"
 "io"
 "net/http"
 "net/url"
 "bytes"
 "time"
 "unicode/utf8"
)

type Request struct { Query string `json:"query"` }
type Source struct { Title string `json:"title"`; URL string `json:"url"`; Content string `json:"content"` }
type Result struct { Sources []Source `json:"sources"`; Credits *float64 `json:"credits,omitempty"` }
type Backend interface { Search(context.Context, Request) (Result, error) }

// Error contains only an allowlisted diagnostic. Never include upstream bodies,
// URLs, headers or transport errors: those may echo credentials or queries.
type Error struct { Code string; Status int }
func (e *Error) Error() string { return e.Code }

type Tavily struct { Endpoint, APIKey string; Timeout time.Duration; MaxResults int; Client *http.Client }
func (t *Tavily) Search(ctx context.Context, q Request) (Result, error) {
 if len(q.Query)==0 || len(q.Query)>4096 { return Result{}, &Error{Code:"invalid_search_query",Status:400} }
 u, err := url.Parse(t.Endpoint)
 if err!=nil || u.Host=="" || (u.Scheme!="https" && u.Scheme!="http") || u.User!=nil || u.RawQuery!="" || u.Fragment!="" || t.APIKey=="" { return Result{}, &Error{Code:"invalid_search_configuration",Status:500} }
 limit:=t.MaxResults; if limit<1 || limit>10 {limit=5}
 body,_:=json.Marshal(map[string]any{"query":q.Query,"topic":"general","search_depth":"basic","include_answer":false,"include_raw_content":false,"max_results":limit})
 timeout:=t.Timeout; if timeout<=0 {timeout=20*time.Second}
 ctx,cancel:=context.WithTimeout(ctx,timeout);defer cancel()
 req,_:=http.NewRequestWithContext(ctx,"POST",u.String(),bytes.NewReader(body))
 req.Header.Set("Content-Type","application/json");req.Header.Set("Authorization","Bearer "+t.APIKey)
 client:=http.Client{}
 if t.Client!=nil {client=*t.Client}
 client.CheckRedirect=func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}
 resp,err:=client.Do(req)
 if err!=nil {code:="search_transport_error";if errors.Is(ctx.Err(),context.DeadlineExceeded){code="search_timeout"};return Result{},&Error{Code:code,Status:502}}
 defer resp.Body.Close()
 if resp.StatusCode!=200 {return Result{}, &Error{Code:"search_upstream_error",Status:resp.StatusCode}}
 const maxBody=1<<20
 b,err:=io.ReadAll(io.LimitReader(resp.Body,maxBody+1));if err!=nil || len(b)>maxBody {return Result{},&Error{Code:"search_response_limit",Status:502}}
 var wire struct { Results []Source `json:"results"`; Usage struct { Credits *float64 `json:"credits"` } `json:"usage"` }
 if json.Unmarshal(b,&wire)!=nil || wire.Results==nil || len(wire.Results)>10 {return Result{},&Error{Code:"invalid_search_response",Status:502}}
 out:=Result{Sources:make([]Source,0,len(wire.Results)),Credits:wire.Usage.Credits}
 for _,s:=range wire.Results {
  parsed,e:=url.Parse(s.URL)
  if e!=nil || parsed.Host=="" || (parsed.Scheme!="https" && parsed.Scheme!="http") || parsed.User!=nil || len(s.URL)>4096 || len(s.Title)>4096 {return Result{},&Error{Code:"invalid_search_source",Status:502}}
  // Limit source text, on a UTF-8 boundary, before injecting it into context.
  if len(s.Content)>12000 {s.Content=s.Content[:12000];for len(s.Content)>0 && !utf8.ValidString(s.Content) {s.Content=s.Content[:len(s.Content)-1]} }
  out.Sources=append(out.Sources,s)
 }
 return out,nil
}
