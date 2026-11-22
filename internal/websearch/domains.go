package websearch

import (
 "fmt"
 "net"
 "strings"
)

// NormalizeDomains accepts DNS names only, never URL/path/wildcard/IP targets.
// Unicode domains must be supplied in their explicit ASCII/punycode form.
func NormalizeDomains(domains []string)([]string,error){
 if len(domains)>100{return nil,fmt.Errorf("domain limit")}
 out:=make([]string,0,len(domains));seen:=map[string]bool{}
 for _,raw:=range domains {
  d:=strings.ToLower(raw)
  if d=="" || strings.TrimSpace(d)!=d || len(d)>253 || strings.HasSuffix(d,".") || !strings.Contains(d,".") || net.ParseIP(d)!=nil{return nil,fmt.Errorf("invalid domain")}
  for _,label:=range strings.Split(d,".") {
   if len(label)==0 || len(label)>63 || label[0]=='-' || label[len(label)-1]=='-' {return nil,fmt.Errorf("invalid domain")}
   for _,c:=range label {if !(c>='a'&&c<='z'||c>='0'&&c<='9'||c=='-'){return nil,fmt.Errorf("invalid domain")}}
  }
  if !seen[d]{seen[d]=true;out=append(out,d)}
 }
 return out,nil
}
func DomainAllowed(host string,domains []string)bool {
 if len(domains)==0{return true}
 host=strings.TrimSuffix(strings.ToLower(host),".")
 for _,d:=range domains {if host==d || strings.HasSuffix(host,"."+d){return true}}
 return false
}
