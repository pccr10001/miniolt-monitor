//go:build linux

package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	listenAddr    = "192.168.1.1:8181"
	interfaceName = "mini-olt"
	etherType     = 0x0701
)

type onu struct {
	ID, Vendor, Serial, EquipmentID, Hardware, Software1, Software2 string
	LOID, LOIDPassword, PLOAMPassword, MAC, ProductClass            string
	LastSeen                                                        time.Time
}

type monitor struct {
	mu      sync.RWMutex
	onus    map[uint16]*onu
	packets uint64
	lastErr string
	started time.Time
}

func main() {
	m := &monitor{onus: make(map[uint16]*onu), started: time.Now()}
	go m.capture()
	http.HandleFunc("/", m.index)
	http.HandleFunc("/api/onus", m.apiONUs)
	http.HandleFunc("/api/clear", m.clear)
	_ = http.ListenAndServe(listenAddr, nil)
}

// capture uses Linux AF_PACKET directly, avoiding tcpdump/libpcap on the OLT.
func (m *monitor) capture() {
	proto := htons(etherType)
	buf := make([]byte, 2048)
	for {
		iface, err := net.InterfaceByName(interfaceName)
		if err != nil {
			m.setError(fmt.Sprintf("waiting for interface %q: %v", interfaceName, err))
			time.Sleep(time.Second)
			continue
		}
		fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(proto))
		if err == nil {
			err = syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: proto, Ifindex: iface.Index})
		}
		if err != nil {
			if fd >= 0 {
				_ = syscall.Close(fd)
			}
			m.setError(fmt.Sprintf("opening %s: %v", interfaceName, err))
			time.Sleep(time.Second)
			continue
		}
		m.clearError()
		for {
			n, _, err := syscall.Recvfrom(fd, buf, 0)
			if err != nil {
				m.setError(fmt.Sprintf("receive from %s: %v", interfaceName, err))
				break
			}
			m.parse(append([]byte(nil), buf[:n]...))
		}
		_ = syscall.Close(fd)
		time.Sleep(time.Second)
	}
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func (m *monitor) setError(err string) {
	m.mu.Lock()
	m.lastErr = err
	m.mu.Unlock()
}

func (m *monitor) clearError() { m.mu.Lock(); m.lastErr = ""; m.mu.Unlock() }

func (m *monitor) parse(frame []byte) {
	// Ethernet (14) + MiniOLT header (50) + baseline OMCI (at least 12 bytes).
	if len(frame) < 76 || binary.BigEndian.Uint16(frame[12:14]) != etherType {
		return
	}
	onuID := binary.BigEndian.Uint16(frame[14:16])
	omci := frame[64:]
	if omci[3] != 0x0a {
		return
	}
	class := binary.BigEndian.Uint16(omci[4:6])
	instance := binary.BigEndian.Uint16(omci[6:8])
	if omci[2] != 0x29 && omci[2] != 0x2e {
		return
	} // Get/MIB-upload responses only.

	m.mu.Lock()
	defer m.mu.Unlock()
	m.packets++
	o := m.onus[onuID]
	if o == nil {
		o = &onu{ID: fmt.Sprint(onuID), PLOAMPassword: "not observed in OMCI"}
		m.onus[onuID] = o
	}
	o.LastSeen = time.Now()
	if omci[2] == 0x29 && len(omci) > 10 && omci[8] != 0 {
		return
	} // Non-success result.
	data := responseData(omci)
	if len(data) == 0 {
		return
	}

	switch class {
	case 0x0007: // Software image: first attribute is the 14-byte version.
		version := printable(data)
		if instance == 0 {
			o.Software1 = version
		} else if instance == 1 {
			o.Software2 = version
		}
	case 0x00f7: // Vendor-specific physical equipment ID; observed H660-C model string.
		o.EquipmentID = printable(data)
		if o.Hardware == "" {
			o.Hardware = o.EquipmentID
		}
	case 0x0101: // Extended ONU-G product class.
		o.ProductClass = printable(data)
	case 0xfffa:
		mask := binary.BigEndian.Uint16(omci[9:11])
		if mask == 0x4000 {
			o.LOID = printable(data)
		}
		if mask == 0x2000 {
			o.LOIDPassword = printable(data)
		}
	case 0x0002: // ONU-G vendor identity; retain raw printable data for vendor/model variants.
		if v := printable(data); v != "" {
			o.Vendor = firstField(v)
		}
	}
	// Some vendor MIB uploads do not use a stable ME schema. Preserve known strings
	// rather than guessing offsets, so new ONU models still become visible.
	for _, s := range strings.FieldsFunc(printable(data), func(r rune) bool { return r == ' ' || r == ',' }) {
		switch {
		case strings.HasPrefix(s, "HW") || strings.Contains(s, "HGW"):
			if o.Hardware == "" {
				o.Hardware = s
			}
		case strings.HasPrefix(s, "I040"), strings.HasPrefix(s, "H660"), strings.HasPrefix(s, "V"):
			if o.Software1 == "" {
				o.Software1 = s
			}
		}
	}
}

// Get responses store result then attribute mask; MIB upload records start with payload.
func responseData(omci []byte) []byte {
	if len(omci) <= 11 {
		return nil
	}
	if omci[2] == 0x29 {
		return omci[11 : len(omci)-4]
	}
	return omci[8 : len(omci)-4]
}

func printable(b []byte) string {
	end := len(b)
	for i, c := range b {
		if c == 0 {
			end = i
			break
		}
	}
	b = b[:end]
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return ""
		}
	}
	return strings.TrimSpace(string(b))
}

func firstField(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

func (m *monitor) snapshot() ([]onu, uint64, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rows := make([]onu, 0, len(m.onus))
	for _, o := range m.onus {
		rows = append(rows, *o)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, m.packets, m.lastErr
}

func (m *monitor) apiONUs(w http.ResponseWriter, r *http.Request) {
	rows, packets, lastErr := m.snapshot()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		ONUs    []onu  `json:"onus"`
		Packets uint64 `json:"packets"`
		Error   string `json:"error"`
	}{rows, packets, lastErr})
}

func (m *monitor) clear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	m.mu.Lock()
	m.onus = make(map[uint16]*onu)
	m.packets = 0
	m.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (m *monitor) index(w http.ResponseWriter, r *http.Request) { page.Execute(w, nil) }

var page = template.Must(template.New("index").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Mini OLT ONU Monitor</title><style>:root{--ink:#102a43;--sea:#087e8b;--sun:#f4b942;--paper:#f7f4ed}*{box-sizing:border-box}body{margin:0;color:var(--ink);font:16px Georgia,serif;background:radial-gradient(circle at 90% 0,#d9f0e7,transparent 32rem),var(--paper)}main{max-width:1500px;margin:auto;padding:3rem 1.5rem}h1{font-size:clamp(2rem,5vw,4.5rem);letter-spacing:-.05em;margin:0}header{display:flex;justify-content:space-between;gap:2rem;align-items:end;border-bottom:4px solid var(--ink);padding-bottom:1.2rem}button{border:2px solid var(--ink);background:var(--sun);padding:.65rem 1rem;font:inherit;font-weight:bold;cursor:pointer}button+button{background:#fff}.note{margin:1.3rem 0;color:#486581}.table-wrap{overflow:auto;background:#fff;border:1px solid #bcccdc}table{border-collapse:collapse;width:100%;min-width:1250px}th{background:var(--ink);color:#fff;text-align:left;font-family:monospace;font-size:.78rem;letter-spacing:.04em}td,th{padding:.8rem;border-bottom:1px solid #d9e2ec;white-space:nowrap}td{font-family:ui-monospace,monospace;font-size:.82rem}tr:nth-child(even){background:#f5faf9}.empty{text-align:center;padding:2rem}#status{font-family:ui-monospace,monospace;font-size:.85rem}@media(max-width:620px){main{padding:1.5rem 1rem}header{display:block}header div{margin-top:1rem}}</style><main><header><div><h1>ONU Registry</h1><p>Live decoding of EtherType 0x0701 / OMCI responses on <code>mini-olt</code></p></div><div><button onclick="refresh()">Refresh now</button><button onclick="clearTable()">Clear table</button></div></header><p class="note">Updates automatically every 5 seconds. A PLOAM password is not guaranteed to be present in this OMCI encapsulation; missing values are shown explicitly.</p><p id="status">Connecting...</p><div class="table-wrap"><table><thead><tr><th>ONU ID</th><th>Vendor</th><th>Serial</th><th>MAC</th><th>Equipment / Model</th><th>HW Version</th><th>SW Image 1</th><th>SW Image 2</th><th>Product Class</th><th>LOID</th><th>LOID Password</th><th>PLOAM Password</th><th>Last Seen</th></tr></thead><tbody id="rows"></tbody></table></div></main><script>const keys=['ID','Vendor','Serial','MAC','EquipmentID','Hardware','Software1','Software2','ProductClass','LOID','LOIDPassword','PLOAMPassword','LastSeen'];function esc(x){const n=document.createElement('span');n.textContent=x||'--';return n.innerHTML}async function refresh(){try{const d=await fetch('/api/onus',{cache:'no-store'}).then(r=>r.json());status.textContent=d.onus.length+' ONU / '+d.packets+' response frames'+(d.error?' / ERROR: '+d.error:'');rows.innerHTML=d.onus.length?d.onus.map(o=>'<tr>'+keys.map(k=>'<td>'+esc(k==='LastSeen'&&o[k]?new Date(o[k]).toLocaleString():o[k])+'</td>').join('')+'</tr>').join(''):'<tr><td class="empty" colspan="13">No ONU OMCI responses received yet</td></tr>'}catch(e){status.textContent='Read failed: '+e}}async function clearTable(){if(confirm('Clear the current in-memory ONU list?')){await fetch('/api/clear',{method:'POST'});refresh()}}refresh();setInterval(refresh,5000)</script></html>`))
