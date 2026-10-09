package linkguard

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

// kernelEncode encodes attributes the way the kernel dumps them back: nests without
// NLA_F_NESTED, nothing the kernel does not dump and an attribute of the kernel's own (a
// handle, a use count) in every object and nest except a rule's expression list.
func kernelEncode(attrs []attr, extra bool) []byte {
	var b []byte
	put := func(typ uint16, data []byte) {
		b = binary.NativeEndian.AppendUint16(b, uint16(4+len(data)))
		b = binary.NativeEndian.AppendUint16(b, typ)
		b = append(b, data...)
		b = append(b, make([]byte, align4(len(data))-len(data))...)
	}
	for _, a := range attrs {
		switch {
		case a.sendOnly:
		case a.list:
			put(a.typ, kernelEncode(a.nested, false))
		case a.nested != nil:
			put(a.typ, kernelEncode(a.nested, true))
		default:
			put(a.typ, a.data)
		}
	}
	if extra {
		put(0x7e, binary.BigEndian.AppendUint64(nil, 42))
	}
	return b
}

// kernelDump is the dump of a table installed from these specs.
func kernelDump(r ruleset, table []attr, sets [][]attr, chains []chainSpec) tableDump {
	d := tableDump{table: kernelEncode(table, true)}
	for _, s := range sets {
		d.sets = append(d.sets, kernelEncode(s, true))
	}
	for _, c := range chains {
		d.chains = append(d.chains, kernelEncode(r.chainAttrs(c), true))
		for _, exprs := range c.rules {
			d.rules = append(d.rules, kernelEncode(r.ruleAttrs(c.name, exprs), true))
		}
	}
	return d
}

func installedDump(r ruleset) tableDump { return kernelDump(r, r.tableAttrs(), r.sets(), r.chains()) }

func encoded(a attr) []byte { return appendAttrs(nil, []attr{a}) }

func TestMessageRoundTrip(t *testing.T) {
	r := ruleset{table: "t", set: "s", gmap: "m"}
	var b []byte
	msgs := r.replace()
	for i, m := range msgs {
		b = m.appendTo(b, uint32(100+i))
	}
	got, err := parseMessages(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(msgs) {
		t.Fatalf("parsed %d messages, sent %d", len(got), len(msgs))
	}
	for i, m := range got {
		if m.typ != nfnlSubsysNFTables<<8|msgs[i].typ || m.seq != uint32(100+i) || m.flags&nlmFRequest == 0 {
			t.Errorf("message %d: type %#x seq %d flags %#x", i, m.typ, m.seq, m.flags)
		}
		if !matchAttrs(msgs[i].attrs, m.attrs()) {
			t.Errorf("message %d does not match its own attributes", i)
		}
	}
	if _, err := parseMessages(b[:len(b)-1]); err == nil {
		t.Error("a truncated read parsed")
	}
}

func TestEncodeBatch(t *testing.T) {
	r := ruleset{table: "t", set: "s", gmap: "m"}
	msgs := r.replace()
	got, err := parseMessages(encodeBatch(msgs, 7))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(msgs)+2 {
		t.Fatalf("%d messages for a batch of %d", len(got), len(msgs))
	}
	begin, end := got[0], got[len(got)-1]
	// The delimiters name the nftables subsystem in nfgenmsg's res_id, in network order.
	if begin.typ != nfnlMsgBatchBegin || begin.seq != 7 || !bytes.Equal(begin.payload, []byte{0, 0, 0, nfnlSubsysNFTables}) {
		t.Errorf("begin: type %#x seq %d payload %x", begin.typ, begin.seq, begin.payload)
	}
	if end.typ != nfnlMsgBatchEnd || end.seq != uint32(8+len(msgs)) || begin.flags&nlmFAck != 0 || end.flags&nlmFAck != 0 {
		t.Errorf("end: type %#x seq %d", end.typ, end.seq)
	}
	for i, m := range got[1 : len(got)-1] {
		if m.seq != uint32(8+i) || m.flags&nlmFAck == 0 || m.flags&^nlmFAck != msgs[i].flags|nlmFRequest {
			t.Errorf("message %d: seq %d flags %#x", i, m.seq, m.flags)
		}
	}
}

func TestReplaceBatch(t *testing.T) {
	r := ruleset{table: links.GuardTable, set: links.GuardSet, gmap: links.GuardMap}
	var kinds []uint16
	for _, m := range r.replace() {
		kinds = append(kinds, m.typ)
	}
	want := []uint16{
		nftMsgNewTable, nftMsgDelTable, nftMsgNewTable,
		nftMsgNewSet, nftMsgNewSet,
		nftMsgNewChain, nftMsgNewChain, nftMsgNewChain,
		nftMsgNewRule, nftMsgNewRule, nftMsgNewRule, nftMsgNewRule,
	}
	if !bytes.Equal(u16s(kinds), u16s(want)) {
		t.Fatalf("replace sends %v, want %v", kinds, want)
	}
	// The set's timeout is GuardTTL in milliseconds.
	if ttl, ok := attrPath(appendAttrs(nil, r.sets()[0]), nftaSetTimeout); !ok || binary.BigEndian.Uint64(ttl) != 1000 {
		t.Errorf("set timeout %x", ttl)
	}
}

func u16s(v []uint16) []byte {
	var b []byte
	for _, x := range v {
		b = binary.BigEndian.AppendUint16(b, x)
	}
	return b
}

func TestRulesetMatches(t *testing.T) {
	r := ruleset{table: links.GuardTable, set: links.GuardSet, gmap: links.GuardMap}
	if !r.matches(installedDump(r)) {
		t.Fatal("the table Install leaves does not match")
	}
	swap := func(blobs [][]byte, from, to []byte) [][]byte {
		var out [][]byte
		for _, b := range blobs {
			out = append(out, bytes.ReplaceAll(b, from, to))
		}
		return out
	}
	tests := []struct {
		name string
		dump func() tableDump
	}{
		{"dormant table", func() tableDump {
			return kernelDump(r, []attr{aStr(nftaTableName, r.table), aU32(nftaTableFlags, 1)}, r.sets(), r.chains())
		}},
		{"another table", func() tableDump {
			other := r
			other.table = "other"
			return installedDump(other)
		}},
		{"set without timeouts", func() tableDump {
			d := installedDump(r)
			d.sets[0] = bytes.Replace(d.sets[0], encoded(aU32(nftaSetFlags, nftSetTimeout)), encoded(aU32(nftaSetFlags, 0)), 1)
			return d
		}},
		{"set with intervals", func() tableDump {
			d := installedDump(r)
			d.sets[0] = bytes.Replace(d.sets[0], encoded(aU32(nftaSetFlags, nftSetTimeout)), encoded(aU32(nftaSetFlags, nftSetTimeout|nftSetInterval)), 1)
			return d
		}},
		{"extra set", func() tableDump {
			d := installedDump(r)
			d.sets = append(d.sets, kernelEncode([]attr{aStr(nftaSetTable, r.table), aStr(nftaSetName, "extra")}, true))
			return d
		}},
		{"missing set", func() tableDump {
			d := installedDump(r)
			d.sets = d.sets[:1]
			return d
		}},
		{"missing chain", func() tableDump {
			return kernelDump(r, r.tableAttrs(), r.sets(), r.chains()[:2])
		}},
		{"chain at another priority", func() tableDump {
			d := installedDump(r)
			prio := int32(links.GuardPriority)
			d.chains = swap(d.chains, encoded(aU32(nftaHookPrio, uint32(prio))), encoded(aU32(nftaHookPrio, uint32(prio+1))))
			return d
		}},
		{"chain policy drop", func() tableDump {
			d := installedDump(r)
			d.chains = swap(d.chains, encoded(aU32(nftaChainPolicy, nfAccept)), encoded(aU32(nftaChainPolicy, nfDrop)))
			return d
		}},
		{"extra rule", func() tableDump {
			chains := r.chains()
			chains[2].rules = append(chains[2].rules, []attr{drop()})
			return kernelDump(r, r.tableAttrs(), r.sets(), chains)
		}},
		{"missing rule", func() tableDump {
			chains := r.chains()
			chains[0].rules = chains[0].rules[:1]
			return kernelDump(r, r.tableAttrs(), r.sets(), chains)
		}},
		{"rules swapped", func() tableDump {
			chains := r.chains()
			chains[0].rules[0], chains[0].rules[1] = chains[0].rules[1], chains[0].rules[0]
			return kernelDump(r, r.tableAttrs(), r.sets(), chains)
		}},
		{"lookup not inverted", func() tableDump {
			chains := r.chains()
			rule := chains[1].rules[0]
			rule[len(rule)-2] = lookup(r.set, reg1, false)
			return kernelDump(r, r.tableAttrs(), r.sets(), chains)
		}},
		{"mark in another register", func() tableDump {
			chains := r.chains()
			rule := chains[0].rules[1]
			rule[len(rule)-3] = metaLoad(nftMetaMark, reg32x01+1)
			return kernelDump(r, r.tableAttrs(), r.sets(), chains)
		}},
		{"expression dropped", func() tableDump {
			chains := r.chains()
			rule := chains[2].rules[0]
			chains[2].rules[0] = rule[:len(rule)-1]
			return kernelDump(r, r.tableAttrs(), r.sets(), chains)
		}},
		{"another version's rules", func() tableDump {
			d := installedDump(r)
			d.rules = swap(d.rules, []byte(ruleTag), []byte("hibernaut-links v0"))
			return d
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if r.matches(tt.dump()) {
				t.Error("matches")
			}
		})
	}
}

func TestElemMessage(t *testing.T) {
	key := []byte{10, 11, 0, 2, 1, 2, 3, 4}
	open := elemMessage("t", elemOp{excl: true, set: "s", key: key, data: []byte{9, 9, 9, 9}, timeout: links.GuardTTL})
	if open.typ != nftMsgNewSetElem || open.flags != nlmFCreate|nlmFExcl {
		t.Errorf("open: type %d flags %#x", open.typ, open.flags)
	}
	extend := elemMessage("t", elemOp{set: "s", key: key, timeout: 1500 * time.Millisecond})
	if extend.flags != nlmFCreate {
		t.Errorf("extend add: flags %#x", extend.flags)
	}
	del := elemMessage("t", elemOp{del: true, set: "s", key: key, timeout: time.Second})
	if del.typ != nftMsgDelSetElem || del.flags != 0 {
		t.Errorf("delete: type %d flags %#x", del.typ, del.flags)
	}
	for _, m := range []message{open, extend, del} {
		b := appendAttrs(nil, m.attrs)
		if attrString(b, nftaSetElemListTable) != "t" || attrString(b, nftaSetElemListSet) != "s" {
			t.Errorf("message %d names the wrong set", m.typ)
		}
		elems, err := parseElements(b)
		if err != nil || len(elems) != 1 || !bytes.Equal(elems[0].key, key) {
			t.Fatalf("elements %v, %v", elems, err)
		}
	}
	elems, _ := parseElements(appendAttrs(nil, open.attrs))
	if !bytes.Equal(elems[0].data, []byte{9, 9, 9, 9}) {
		t.Errorf("open data %x", elems[0].data)
	}
	timeout := func(m message) (uint64, bool) {
		v, ok := attrPath(appendAttrs(nil, m.attrs), nftaSetElemListElements, nftaListElem, nftaSetElemTimeout)
		if !ok {
			return 0, false
		}
		return binary.BigEndian.Uint64(v), true
	}
	if ms, ok := timeout(extend); !ok || ms != 1500 {
		t.Errorf("extend timeout %d %t", ms, ok)
	}
	if _, ok := timeout(del); ok {
		t.Error("a delete carries a timeout")
	}
}

func TestErrno(t *testing.T) {
	ack := binary.NativeEndian.AppendUint32(nil, 0)
	enoent := binary.NativeEndian.AppendUint32(nil, uint32(0xfffffffe))
	if e := (nlmsg{typ: nlmsgError, payload: ack}).errno(); e != 0 {
		t.Errorf("ack errno %d", e)
	}
	if e := (nlmsg{typ: nlmsgError, payload: enoent}).errno(); e != 2 {
		t.Errorf("ENOENT errno %d", e)
	}
}

func TestBatchAcks(t *testing.T) {
	// begin 10, three messages 11 to 13, end 14.
	ack := func(seq uint32, e syscall.Errno) nlmsg {
		return nlmsg{typ: nlmsgError, seq: seq, payload: binary.NativeEndian.AppendUint32(nil, uint32(-int32(e)))}
	}
	timeout := fmt.Errorf("read from nfnetlink: %w", syscall.EAGAIN)
	for _, tc := range []struct {
		name string
		acks []nlmsg
		// timeout is a read that times out after the acks.
		timeout bool
		want    error
		say     string
	}{
		{name: "every message succeeds", acks: []nlmsg{ack(11, 0), ack(12, 0), ack(13, 0)}},
		{name: "first error wins", acks: []nlmsg{ack(11, 0), ack(12, syscall.EEXIST), ack(13, syscall.ENOENT)}, want: syscall.EEXIST, say: "message 2 of 3"},
		{name: "others ignored", acks: []nlmsg{{typ: 0x10, seq: 11}, ack(9, syscall.EPERM), ack(20, syscall.EPERM), ack(11, 0), ack(14, 0), ack(12, 0), ack(13, 0)}},
		{name: "error against begin", acks: []nlmsg{ack(10, syscall.EPERM)}, want: syscall.EPERM},
		{name: "error against end", acks: []nlmsg{ack(14, syscall.EINVAL)}, want: syscall.EINVAL},
		{name: "failed commit", acks: []nlmsg{ack(11, syscall.EBUSY), ack(11, 0), ack(12, 0), ack(13, 0)}, want: syscall.EBUSY, say: "commit"},
		{name: "enomem", acks: []nlmsg{ack(11, syscall.ENOMEM)}, want: syscall.ENOMEM},
		{name: "enomem after an error", acks: []nlmsg{ack(11, syscall.ENOENT), ack(11, syscall.ENOMEM)}, want: syscall.ENOENT},
		{name: "timeout after a failure", acks: []nlmsg{ack(11, 0), ack(12, syscall.ENOENT)}, timeout: true, want: syscall.ENOENT, say: "message 2 of 3"},
		{name: "timeout before any failure", acks: []nlmsg{ack(11, 0)}, timeout: true, want: syscall.EAGAIN},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newBatchAcks(10, 3)
			for i, m := range tc.acks {
				if a.done() {
					t.Fatalf("done before ack %d", i)
				}
				a.add(m)
			}
			err := a.err()
			if tc.timeout {
				if a.done() {
					t.Fatal("done before the read that times out")
				}
				err = a.recvFailed(timeout)
			} else if !a.done() {
				t.Fatal("still waiting after the last ack")
			}
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !strings.Contains(fmt.Sprint(err), tc.say) {
				t.Fatalf("err = %v, want it to say %q", err, tc.say)
			}
		})
	}
}
