package ebpf

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Key mirrors struct traffic_key in bpf/traffic.c (12 bytes, explicit padding).
type Key struct {
	Ifindex uint32
	MAC     [6]byte
	Pad     uint16
}

// Value mirrors struct traffic_value. Directions are from the LAN device's
// point of view: Up = sent by the device (tc ingress), Down = received by the
// device (tc egress).
type Value struct {
	UpBytes     uint64
	UpPackets   uint64
	DownBytes   uint64
	DownPackets uint64
}

func (v Value) Add(o Value) Value {
	return Value{v.UpBytes + o.UpBytes, v.UpPackets + o.UpPackets, v.DownBytes + o.DownBytes, v.DownPackets + o.DownPackets}
}

// Sample is one map entry with its per-CPU values summed.
type Sample struct {
	Key   Key
	Value Value
}

// IfaceStatus describes the attachment of one configured interface.
type IfaceStatus struct {
	Name     string `json:"name"`
	Ifindex  int    `json:"ifindex"`
	Attached bool   `json:"attached"`
	Mode     string `json:"mode,omitempty"` // "tcx" or "tc"
	Error    string `json:"error,omitempty"`
}

type attachment struct {
	status IfaceStatus
	links  []link.Link
	tc     []*netlink.BpfFilter
}

// Collector owns the loaded BPF objects and their attachments.
type Collector struct {
	objs trafficObjects
	logf func(string, ...any)

	mu      sync.Mutex
	ifaces  map[string]*attachment
	names   map[uint32]string // ifindex -> interface name, kept after detach
	forceTC bool              // TRAFFIC_COUNT_FORCE_TC=1: skip TCX, use clsact/cls_bpf (testing)
}

// Load loads the embedded BPF programs and creates the counter map.
func Load() (*Collector, error) {
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("remove memlock rlimit: %w", err)
	}
	c := &Collector{
		logf:   log.Printf,
		ifaces: map[string]*attachment{},
		names:  map[uint32]string{},
	}
	c.forceTC = os.Getenv("TRAFFIC_COUNT_FORCE_TC") == "1"
	if err := loadTrafficObjects(&c.objs, nil); err != nil {
		var verr *ebpf.VerifierError
		if errors.As(err, &verr) {
			return nil, fmt.Errorf("load BPF objects: %+v", verr)
		}
		return nil, fmt.Errorf("load BPF objects: %w", err)
	}
	return c, nil
}

// Close detaches everything and releases the BPF objects.
func (c *Collector) Close() error {
	c.mu.Lock()
	for name := range c.ifaces {
		c.detachLocked(name)
	}
	c.mu.Unlock()
	return c.objs.Close()
}

// Attach (re)attaches both programs to the named interface. It is idempotent:
// an interface that is already attached at the same ifindex is left alone,
// and one whose ifindex changed (deleted and re-created) is re-attached.
func (c *Collector) Attach(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attachLocked(name)
}

func (c *Collector) attachLocked(name string) error {
	a := c.ifaces[name]
	if a == nil {
		a = &attachment{status: IfaceStatus{Name: name}}
		c.ifaces[name] = a
	}

	lnk, err := netlink.LinkByName(name)
	if err != nil {
		c.detachLocked(name)
		a.status.Error = fmt.Sprintf("interface not found: %v", err)
		return fmt.Errorf("interface %q: %w", name, err)
	}
	attrs := lnk.Attrs()
	if a.status.Attached && a.status.Ifindex == attrs.Index {
		return nil
	}
	c.detachLocked(name)
	a.status.Ifindex = attrs.Index

	if attrs.EncapType != "ether" || len(attrs.HardwareAddr) != 6 {
		a.status.Error = fmt.Sprintf("not an Ethernet interface (link type %q); per-MAC accounting needs an L2 interface such as a bridge", attrs.EncapType)
		return errors.New(a.status.Error)
	}

	if !c.forceTC {
		if err := c.attachTCX(a, attrs.Index); err == nil {
			c.names[uint32(attrs.Index)] = name
			return nil
		} else if !errors.Is(err, ebpf.ErrNotSupported) {
			a.status.Error = err.Error()
			return err
		}
	}
	if err := c.attachTC(a, lnk); err != nil {
		a.status.Error = err.Error()
		return err
	}
	c.names[uint32(attrs.Index)] = name
	return nil
}

func (c *Collector) attachTCX(a *attachment, ifindex int) error {
	in, err := link.AttachTCX(link.TCXOptions{Interface: ifindex, Program: c.objs.CountIngress, Attach: ebpf.AttachTCXIngress})
	if err != nil {
		return fmt.Errorf("attach tcx ingress: %w", err)
	}
	out, err := link.AttachTCX(link.TCXOptions{Interface: ifindex, Program: c.objs.CountEgress, Attach: ebpf.AttachTCXEgress})
	if err != nil {
		in.Close()
		return fmt.Errorf("attach tcx egress: %w", err)
	}
	a.links = []link.Link{in, out}
	a.status.Attached, a.status.Mode, a.status.Error = true, "tcx", ""
	return nil
}

// tcPriority is the cls_bpf filter priority used in legacy mode; it is also
// how stale filters from a previous run are recognised and replaced.
const tcPriority = 0xC0DE

// attachTC is the fallback for kernels without TCX (< 6.6): clsact qdisc +
// direct-action cls_bpf filters.
func (c *Collector) attachTC(a *attachment, lnk netlink.Link) error {
	qdisc := &netlink.GenericQdisc{
		QdiscAttrs: netlink.QdiscAttrs{
			LinkIndex: lnk.Attrs().Index,
			Handle:    netlink.MakeHandle(0xffff, 0),
			Parent:    netlink.HANDLE_CLSACT,
		},
		QdiscType: "clsact",
	}
	if err := netlink.QdiscReplace(qdisc); err != nil {
		return fmt.Errorf("add clsact qdisc: %w", err)
	}
	for _, d := range []struct {
		parent uint32
		prog   *ebpf.Program
		name   string
	}{
		{netlink.HANDLE_MIN_INGRESS, c.objs.CountIngress, "wlt_traffic_in"},
		{netlink.HANDLE_MIN_EGRESS, c.objs.CountEgress, "wlt_traffic_out"},
	} {
		f := &netlink.BpfFilter{
			FilterAttrs: netlink.FilterAttrs{
				LinkIndex: lnk.Attrs().Index,
				Parent:    d.parent,
				Handle:    1,
				Priority:  tcPriority,
				Protocol:  unix.ETH_P_ALL,
			},
			Fd:           d.prog.FD(),
			Name:         d.name,
			DirectAction: true,
		}
		if err := netlink.FilterReplace(f); err != nil {
			for _, old := range a.tc {
				netlink.FilterDel(old)
			}
			a.tc = nil
			return fmt.Errorf("add tc filter: %w", err)
		}
		a.tc = append(a.tc, f)
	}
	a.status.Attached, a.status.Mode, a.status.Error = true, "tc", ""
	return nil
}

func (c *Collector) detachLocked(name string) {
	a := c.ifaces[name]
	if a == nil {
		return
	}
	for _, l := range a.links {
		l.Close()
	}
	for _, f := range a.tc {
		netlink.FilterDel(f) // fails harmlessly if the device is gone
	}
	a.links, a.tc = nil, nil
	a.status.Attached, a.status.Mode = false, ""
}

// Detach removes the programs from the named interface.
func (c *Collector) Detach(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detachLocked(name)
}

// Status returns the attachment state of every interface Attach was called for.
func (c *Collector) Status() []IfaceStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]IfaceStatus, 0, len(c.ifaces))
	for _, a := range c.ifaces {
		out = append(out, a.status)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NameOf maps an ifindex seen in the counter map to the configured interface
// name it belonged to when the counters were taken.
func (c *Collector) NameOf(ifindex uint32) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n, ok := c.names[ifindex]; ok {
		return n
	}
	return fmt.Sprintf("if%d", ifindex)
}

// Watch re-attaches configured interfaces when they are re-created or come
// back up (bridges are often torn down and rebuilt by network managers, which
// destroys the TCX links). It also retries periodically. Blocks until ctx ends.
func (c *Collector) Watch(ctx context.Context, names []string, onChange func()) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	retry := time.NewTicker(30 * time.Second)
	defer retry.Stop()

	for ctx.Err() == nil {
		updates := make(chan netlink.LinkUpdate, 64)
		done := make(chan struct{})
		if err := netlink.LinkSubscribeWithOptions(updates, done, netlink.LinkSubscribeOptions{
			ErrorCallback: func(err error) {
				if ctx.Err() == nil {
					c.logf("link monitor: %v", err)
				}
			},
		}); err != nil {
			c.logf("link monitor: subscribe: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		c.reattach(names, onChange)
	loop:
		for {
			select {
			case <-ctx.Done():
				close(done)
				return
			case <-retry.C:
				c.reattach(names, onChange)
			case u, ok := <-updates:
				if !ok {
					break loop
				}
				if u.Link == nil || !want[u.Link.Attrs().Name] {
					continue
				}
				name := u.Link.Attrs().Name
				if u.Header.Type == unix.RTM_DELLINK {
					c.mu.Lock()
					c.detachLocked(name)
					if a := c.ifaces[name]; a != nil {
						a.status.Error = "interface removed; waiting for it to come back"
					}
					c.mu.Unlock()
					c.logf("interface %s removed", name)
					if onChange != nil {
						onChange()
					}
					continue
				}
				c.reattach([]string{name}, onChange)
			}
		}
		close(done)
	}
}

func (c *Collector) reattach(names []string, onChange func()) {
	for _, n := range names {
		c.mu.Lock()
		before := IfaceStatus{}
		if a := c.ifaces[n]; a != nil {
			before = a.status
		}
		err := c.attachLocked(n)
		after := c.ifaces[n].status
		c.mu.Unlock()
		if before.Attached != after.Attached || before.Ifindex != after.Ifindex {
			if err != nil {
				c.logf("interface %s: %v", n, err)
			} else {
				c.logf("interface %s attached (ifindex %d, %s)", n, after.Ifindex, after.Mode)
			}
			if onChange != nil {
				onChange()
			}
		}
	}
}

// Snapshot reads every counter, summing the per-CPU values.
func (c *Collector) Snapshot() ([]Sample, error) {
	var (
		key    Key
		percpu []Value
		out    []Sample
	)
	it := c.objs.TrafficMap.Iterate()
	for it.Next(&key, &percpu) {
		var sum Value
		for _, v := range percpu {
			sum = sum.Add(v)
		}
		out = append(out, Sample{Key: key, Value: sum})
	}
	if err := it.Err(); err != nil {
		return out, fmt.Errorf("iterate traffic map: %w", err)
	}
	return out, nil
}

// Delete removes counter entries (used for idle devices).
func (c *Collector) Delete(keys []Key) error {
	var errs []error
	for _, k := range keys {
		if err := c.objs.TrafficMap.Delete(k); err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// MACString formats a MAC as lower-case colon-separated hex.
func MACString(mac [6]byte) string {
	return net.HardwareAddr(mac[:]).String()
}
