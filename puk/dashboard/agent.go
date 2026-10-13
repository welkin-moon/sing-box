//go:build linux

package main

import (
 "context"
 "crypto/subtle"
 "encoding/json"
 "fmt"
 "net/http"
 "os"
 "os/exec"
 "path/filepath"
 "regexp"
 "sync"
 "syscall"
 "time"
)

const stateDir = "/var/lib/puk-dashboard"
type Job struct { ID string `json:"id"`; Status string `json:"status"`; Started int64 `json:"started"`; Finished int64 `json:"finished,omitempty"`; Result json.RawMessage `json:"result,omitempty"` }
type Agent struct { mu sync.Mutex; snapshot map[string]any; job Job; key string; region string }
func run(seconds int, program string, args ...string) ([]byte,error) { ctx,cancel:=context.WithTimeout(context.Background(),time.Duration(seconds)*time.Second);defer cancel();return exec.CommandContext(ctx,program,args...).Output() }
func query(sql string) (json.RawMessage,error) { b,e:=run(5,"sqlite3","-readonly","-json","/usr/local/s-ui/db/s-ui.db",sql);if len(b)==0 {b=[]byte("[]")};return b,e }
func writeJSON(w http.ResponseWriter, status int, value any) {w.Header().Set("Content-Type","application/json");w.Header().Set("Cache-Control","no-store");w.WriteHeader(status);json.NewEncoder(w).Encode(value)}
func (a *Agent) sample() {
 users,e:=query("SELECT id,name,enable,volume,expiry,up,down,total_up,total_down,online_at FROM clients ORDER BY name;")
 if e!=nil {a.mu.Lock();a.snapshot=map[string]any{"region":a.region,"error":"statistics unavailable","sampled_at":time.Now().Unix()};a.mu.Unlock();return}
 nodes,e:=query("SELECT i.tag,i.type,COALESCE(sum(CASE WHEN s.direction=0 THEN s.traffic ELSE 0 END),0) AS direction0,COALESCE(sum(CASE WHEN s.direction=1 THEN s.traffic ELSE 0 END),0) AS direction1 FROM inbounds i LEFT JOIN stats s ON s.resource='inbound' AND s.tag=i.tag GROUP BY i.id ORDER BY i.id;")
 if e!=nil {nodes=json.RawMessage("[]")}
 window,_:=query("SELECT min(date_time) AS from_time,max(date_time) AS to_time FROM stats WHERE resource='inbound';")
 svc,_:=run(3,"systemctl","is-active","s-ui")
 a.mu.Lock();old:=a.snapshot;v:=map[string]any{"region":a.region,"sampled_at":time.Now().Unix(),"service":string(svc),"users":users,"nodes":nodes,"window":window};if old!=nil {v["warp"]=old["warp"]};a.snapshot=v;a.mu.Unlock()
}
func (a *Agent) probe() {b,e:=run(50,"/usr/local/sbin/puk-dashboard-probe");var v any;if e!=nil||json.Unmarshal(b,&v)!=nil {v=map[string]any{"ok":false,"error":"HTTPS probe failed","checked_at":time.Now().Unix()}};a.mu.Lock();if a.snapshot!=nil {a.snapshot["warp"]=v};a.mu.Unlock()}
func (a *Agent) save() { b,_:=json.Marshal(a.job);p:=filepath.Join(stateDir,"job.json");os.WriteFile(p+".tmp",b,0600);os.Rename(p+".tmp",p) }
var idRE=regexp.MustCompile(`^[a-zA-Z0-9_-]{16,64}$`)
func (a *Agent) handler(w http.ResponseWriter,r *http.Request) {
 if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")),[]byte("Bearer "+a.key))!=1 {writeJSON(w,401,map[string]any{"error":"unauthorized"});return}
 switch {
 case r.URL.Path=="/status"&&r.Method=="GET":a.mu.Lock();defer a.mu.Unlock();writeJSON(w,200,map[string]any{"node":a.snapshot,"job":a.job})
 case r.URL.Path=="/rotate"&&r.Method=="POST":
  id:=r.Header.Get("Idempotency-Key");if !idRE.MatchString(id){writeJSON(w,400,map[string]any{"error":"invalid request ID"});return}
  a.mu.Lock();defer a.mu.Unlock()
  if a.job.ID==id {writeJSON(w,200,a.job);return}
  if a.job.Status=="running" {writeJSON(w,409,map[string]any{"error":"rotation in progress","job":a.job});return}
  if time.Now().Unix()-a.job.Started<600 {writeJSON(w,429,map[string]any{"error":"wait 10 minutes between attempts","job":a.job});return}
  lock,err:=os.OpenFile(filepath.Join(stateDir,"rotation.lock"),os.O_CREATE|os.O_RDWR,0600);if err!=nil {writeJSON(w,500,map[string]any{"error":"lock unavailable"});return}
  if syscall.Flock(int(lock.Fd()),syscall.LOCK_EX|syscall.LOCK_NB)!=nil {lock.Close();writeJSON(w,409,map[string]any{"error":"rotation already locked"});return}
  a.job=Job{ID:id,Status:"running",Started:time.Now().Unix()};a.save();writeJSON(w,202,a.job)
  go func(){defer lock.Close();b,e:=run(240,"/usr/local/sbin/puk-warp-rotate",id);a.mu.Lock();a.job.Finished=time.Now().Unix();a.job.Status="done";if e!=nil {a.job.Status="failed"};if json.Valid(b){a.job.Result=b}else{a.job.Result=json.RawMessage(`{"error":"rotation failed; consult local rollback record"}`)};a.save();a.mu.Unlock();a.probe()}()
 default:writeJSON(w,404,map[string]any{"error":"not found"})
 }
}
func main(){key,e:=os.ReadFile("/etc/puk-dashboard/token");if e!=nil||len(key)<32 {panic("missing agent token")};region,_:=os.ReadFile("/etc/puk-dashboard/region");a:=&Agent{key:string(key),region:string(region)};os.MkdirAll(stateDir,0700);b,_:=os.ReadFile(filepath.Join(stateDir,"job.json"));json.Unmarshal(b,&a.job);if a.job.Status=="running" {a.job.Status="interrupted";a.save()};a.sample();go a.probe();go func(){t:=time.NewTicker(15*time.Second);for range t.C {a.sample()}}();go func(){t:=time.NewTicker(90*time.Second);for range t.C{a.probe()}}();s:=http.Server{Addr:"127.0.0.1:18190",Handler:http.HandlerFunc(a.handler),ReadHeaderTimeout:5*time.Second,ReadTimeout:10*time.Second,WriteTimeout:15*time.Second,MaxHeaderBytes:8192};fmt.Println("PUK agent listening on loopback");if e=s.ListenAndServe();e!=nil{panic(e)}}
