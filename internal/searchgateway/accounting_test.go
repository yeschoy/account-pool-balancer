package searchgateway

import (
 "context"
 "encoding/json"
 "io"
 "net/http"
 "net/http/httptest"
 "strings"
 "sync/atomic"
 "testing"

 "account-pool-balancer/internal/websearch"
)

func TestResponseEchoesBudgetAndForcedSearch(t *testing.T){
 var calls atomic.Int32
 s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  raw,_:=io.ReadAll(r.Body);o,e:=decodeObject(raw);if e!=nil{t.Error(e)};n:=calls.Add(1)
  out:=[]json.RawMessage{searchCall("one")};if n==2{out=[]json.RawMessage{answer("See https://example.com/docs")};if string(o["max_output_tokens"])!="7"||string(o["tool_choice"])!=`"auto"`{t.Errorf("continuation budget/forced choice: %s",raw)}}
  // Real Responses implementations echo tools and request options.
  _=json.NewEncoder(w).Encode(object{"id":marshal("upstream"),"status":marshal("completed"),"output":marshal(out),"tools":o["tools"],"tool_choice":o["tool_choice"],"max_output_tokens":o["max_output_tokens"],"usage":marshal(map[string]int{"input_tokens":10,"output_tokens":3,"total_tokens":13})})
 }));defer s.Close();g:=startGateway(t,configFor(t,s.URL,"compat"),searchFunc(searchOK))
 body:=strings.Replace(requestJSON,`"input":"question"`,`"input":"question","max_output_tokens":10,"tool_choice":{"type":"web_search"}`,1)
 w:=do(t,g,"route0",body,"s");o,_:=decodeObject(w.Body.Bytes());if w.Code!=200||calls.Load()!=2||string(o["max_output_tokens"])!="10"||!strings.Contains(string(o["tool_choice"]),"web_search")||strings.Contains(w.Body.String(),searchFunction){t.Fatalf("internal response echo: %d %s",w.Code,w.Body.String())}
}
func TestConfiguredCredentialsNeverSentAsQuery(t *testing.T){
 var searches atomic.Int32;s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){call,_:=decodeObject(searchCall("bad"));call["arguments"]=marshal(string(marshal(map[string]string{"query":"please search "+fakeUpstream})));reply(w,[]json.RawMessage{marshal(call)},"completed")}));defer s.Close();g:=startGateway(t,configFor(t,s.URL,"compat"),searchFunc(func(context.Context,websearch.Request)(websearch.Result,error){searches.Add(1);return websearch.Result{},nil}));w:=do(t,g,"route0",requestJSON,"s");if w.Code!=400||searches.Load()!=0||strings.Contains(w.Body.String(),fakeUpstream){t.Fatalf("credential search: %d %s",w.Code,w.Body.String())}
}
func TestRequestAndContinuationLimits(t *testing.T){
 var calls atomic.Int32;s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){calls.Add(1);reply(w,[]json.RawMessage{searchCall("a")},"completed")}));defer s.Close();c:=configFor(t,s.URL,"compat");c.MaxBodyBytes=1024;g:=startGateway(t,c,searchFunc(searchOK));w:=do(t,g,"route0",strings.Repeat("x",1025),"s");if w.Code!=413||calls.Load()!=0{t.Fatal("oversized body contacted upstream")}
 g.search=searchFunc(func(context.Context,websearch.Request)(websearch.Result,error){return websearch.Result{Sources:[]websearch.Source{{URL:"https://example.com",Content:strings.Repeat("x",2000)}}},nil});w=do(t,g,"route0",requestJSON,"s");if w.Code!=413||calls.Load()!=1{t.Fatalf("continuation body cap: %d %s",w.Code,w.Body.String())}
}
