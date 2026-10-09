package linkguard

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

// The numbers below are from linux/netlink.h, linux/netfilter/nfnetlink.h,
// linux/netfilter/nf_tables.h and libnftnl's udata.h. They are spelled out rather than taken
// from golang.org/x/sys/unix, which defines them only on Linux, so the encoding and the
// comparison that Installed rests on are tested on any OS.
const (
	nlmsgHdrLen = 16
	nfgenLen    = 4

	nlmsgError = 0x2
	nlmsgDone  = 0x3

	nlmFRequest  = 0x1
	nlmFAck      = 0x4
	nlmFDumpIntr = 0x10
	nlmFDump     = 0x300
	nlmFExcl     = 0x200
	nlmFCreate   = 0x400
	nlmFAppend   = 0x800

	nlaFNested       = 0x8000
	nlaFNetByteorder = 0x4000
	nlaTypeMask      = ^uint16(nlaFNested | nlaFNetByteorder)

	nfnlSubsysNFTables = 10
	nfnlMsgBatchBegin  = 0x10
	nfnlMsgBatchEnd    = 0x11
	nfprotoINET        = 1
	nfprotoIPv4        = 2

	nftMsgNewTable   = 0
	nftMsgGetTable   = 1
	nftMsgDelTable   = 2
	nftMsgNewChain   = 3
	nftMsgGetChain   = 4
	nftMsgNewRule    = 6
	nftMsgGetRule    = 7
	nftMsgNewSet     = 9
	nftMsgGetSet     = 10
	nftMsgNewSetElem = 12
	nftMsgGetSetElem = 13
	nftMsgDelSetElem = 14

	nftaTableName  = 1
	nftaTableFlags = 2

	nftaChainTable  = 1
	nftaChainName   = 3
	nftaChainHook   = 4
	nftaChainPolicy = 5
	nftaChainType   = 7
	nftaHookHooknum = 1
	nftaHookPrio    = 2

	nftaRuleTable    = 1
	nftaRuleChain    = 2
	nftaRuleExprs    = 4
	nftaRuleUserdata = 7

	nftaListElem = 1
	nftaExprName = 1
	nftaExprData = 2

	nftaSetTable    = 1
	nftaSetName     = 2
	nftaSetFlags    = 3
	nftaSetKeyType  = 4
	nftaSetKeyLen   = 5
	nftaSetDataType = 6
	nftaSetDataLen  = 7
	nftaSetID       = 10
	nftaSetTimeout  = 11

	nftSetInterval = 0x4
	nftSetMap      = 0x8
	nftSetTimeout  = 0x10

	nftaSetElemListTable    = 1
	nftaSetElemListSet      = 2
	nftaSetElemListElements = 3
	nftaSetElemKey          = 1
	nftaSetElemData         = 2
	nftaSetElemTimeout      = 4

	nftaDataValue   = 1
	nftaDataVerdict = 2
	nftaVerdictCode = 1

	nftaPayloadDreg   = 1
	nftaPayloadBase   = 2
	nftaPayloadOffset = 3
	nftaPayloadLen    = 4
	nftPayloadNetwork = 1

	nftaBitwiseSreg = 1
	nftaBitwiseDreg = 2
	nftaBitwiseLen  = 3
	nftaBitwiseMask = 4
	nftaBitwiseXor  = 5

	nftaCmpSreg = 1
	nftaCmpOp   = 2
	nftaCmpData = 3
	nftCmpEq    = 0

	nftaLookupSet   = 1
	nftaLookupSreg  = 2
	nftaLookupDreg  = 3
	nftaLookupFlags = 5
	nftLookupFInv   = 1

	nftaMetaDreg   = 1
	nftaMetaKey    = 2
	nftaMetaSreg   = 3
	nftMetaMark    = 3
	nftMetaNfproto = 15
	nftMetaL4proto = 16

	nftaImmediateDreg = 1
	nftaImmediateData = 2

	nftaSocketKey  = 1
	nftaSocketDreg = 2
	nftSocketMark  = 1

	// Registers as nft writes them: the first 32-bit slot of a concatenation is the legacy
	// 128-bit register 1 and the second is NFT_REG32_01. The kernel dumps them back in the
	// same form.
	regVerdict = 0
	reg1       = 1
	reg32x01   = 9

	nfDrop   = 0
	nfAccept = 1

	hookPrerouting = 0
	hookInput      = 1
	hookOutput     = 3

	// nft's own type numbers for ipv4_addr and mark, and the shift it builds a
	// concatenation's type with. The kernel stores them without reading them; they are what
	// lets nft list the table.
	typeIPv4Addr   = 7
	typeMark       = 19
	concatTypeBits = 6

	udataRuleComment = 0
)

// ruleTag is the comment every rule carries. Installed compares it, so a table left by another
// version of the rules reads as not installed and is replaced.
const ruleTag = "hibernaut-links v1"

// attr is a netlink attribute to send, or the expected shape of one to compare against a dump.
type attr struct {
	typ    uint16
	data   []byte
	nested []attr
	// list marks a nest whose children are compared one for one and in order, as a rule's
	// expressions are, rather than as a subset.
	list bool
	// sendOnly is an attribute the kernel takes but does not dump back.
	sendOnly bool
}

func aStr(typ uint16, s string) attr { return attr{typ: typ, data: append([]byte(s), 0)} }

func aU32(typ uint16, v uint32) attr {
	return attr{typ: typ, data: binary.BigEndian.AppendUint32(nil, v)}
}

func aU64(typ uint16, v uint64) attr {
	return attr{typ: typ, data: binary.BigEndian.AppendUint64(nil, v)}
}

func aBytes(typ uint16, b []byte) attr { return attr{typ: typ, data: b} }

func aNest(typ uint16, children ...attr) attr { return attr{typ: typ, nested: children} }

func align4(n int) int { return (n + 3) &^ 3 }

func appendAttrs(b []byte, attrs []attr) []byte {
	for _, a := range attrs {
		typ, data := a.typ, a.data
		if a.nested != nil || a.list {
			typ |= nlaFNested
			data = appendAttrs(nil, a.nested)
		}
		b = binary.NativeEndian.AppendUint16(b, uint16(4+len(data)))
		b = binary.NativeEndian.AppendUint16(b, typ)
		b = append(b, data...)
		b = append(b, make([]byte, align4(len(data))-len(data))...)
	}
	return b
}

// message is one nfnetlink request. typ is an nftables message type, which the encoder puts
// under the nftables subsystem, unless raw is set, as it is for the batch delimiters.
type message struct {
	typ    uint16
	flags  uint16
	family uint8
	resID  uint16
	raw    bool
	attrs  []attr
}

func (m message) appendTo(b []byte, seq uint32) []byte {
	payload := appendAttrs(nil, m.attrs)
	typ := m.typ
	if !m.raw {
		typ |= nfnlSubsysNFTables << 8
	}
	b = binary.NativeEndian.AppendUint32(b, uint32(nlmsgHdrLen+nfgenLen+len(payload)))
	b = binary.NativeEndian.AppendUint16(b, typ)
	b = binary.NativeEndian.AppendUint16(b, m.flags|nlmFRequest)
	b = binary.NativeEndian.AppendUint32(b, seq)
	b = binary.NativeEndian.AppendUint32(b, 0)
	b = append(b, m.family, 0)
	b = binary.BigEndian.AppendUint16(b, m.resID)
	return append(b, payload...)
}

var (
	batchBegin = message{typ: nfnlMsgBatchBegin, raw: true, resID: nfnlSubsysNFTables}
	batchEnd   = message{typ: nfnlMsgBatchEnd, raw: true, resID: nfnlSubsysNFTables}
)

// encodeBatch frames msgs as one nftables transaction, each asking for an acknowledgement.
// The begin delimiter takes seq, the messages the numbers after it and the end delimiter
// seq+len(msgs)+1.
func encodeBatch(msgs []message, seq uint32) []byte {
	b := batchBegin.appendTo(nil, seq)
	for i, m := range msgs {
		m.flags |= nlmFAck
		b = m.appendTo(b, seq+1+uint32(i))
	}
	return batchEnd.appendTo(b, seq+1+uint32(len(msgs)))
}

// enomem is ENOMEM in asm-generic/errno-base.h.
const enomem = 12

// batchAcks follows the acknowledgements of a batch framed by encodeBatch(msgs, begin), as
// nfnetlink_rcv_batch sends them. The kernel processes the whole batch first, then sends one
// ack per message, zero or an errno, in order. A failed commit puts one more error, against the
// first message, ahead of them. Two cases send nothing more: an error against a delimiter, which
// refuses the whole batch, and ENOMEM, on which the kernel drops the acks it had queued and
// reports ENOMEM against the first message alone.
type batchAcks struct {
	begin  uint32
	seen   []bool
	left   int
	errno  syscall.Errno
	at     int
	commit bool
	final  error
}

func newBatchAcks(begin uint32, n int) *batchAcks {
	return &batchAcks{begin: begin, seen: make([]bool, n), left: n}
}

// add takes one message read back after the batch was sent.
func (a *batchAcks) add(m nlmsg) {
	if a.done() || m.typ != nlmsgError {
		return
	}
	e := syscall.Errno(m.errno())
	if m.seq == a.begin || m.seq == a.begin+1+uint32(len(a.seen)) {
		if e != 0 {
			a.final = fmt.Errorf("nftables transaction: %w", e)
		}
		return
	}
	n := m.seq - a.begin - 1
	if n >= uint32(len(a.seen)) {
		return
	}
	i := int(n)
	if e == enomem {
		if a.final = a.err(); a.final == nil {
			a.final = fmt.Errorf("nftables transaction: %w", e)
		}
		return
	}
	if !a.seen[i] {
		a.seen[i] = true
		a.left--
	}
	switch {
	case e != 0 && a.errno == 0:
		a.errno, a.at = e, i
	case e == 0 && a.errno != 0 && i == a.at:
		// The message that failed is acked as well, so the error was the commit's.
		a.commit = true
	}
}

// done reports whether the kernel will send nothing more about the batch.
func (a *batchAcks) done() bool {
	return a.final != nil || a.left == 0
}

// err returns the batch's first error, wrapping the kernel's errno.
func (a *batchAcks) err() error {
	switch {
	case a.final != nil:
		return a.final
	case a.errno == 0:
		return nil
	case a.commit:
		return fmt.Errorf("nftables transaction commit: %w", a.errno)
	default:
		return fmt.Errorf("nftables transaction, message %d of %d: %w", a.at+1, len(a.seen), a.errno)
	}
}

// recvFailed returns what to report when reading the acks fails with err: the batch's own
// error, if one has arrived, since a read that times out after it is the kernel having nothing
// more to say.
func (a *batchAcks) recvFailed(err error) error {
	if failed := a.err(); failed != nil {
		return failed
	}
	return err
}

// nlmsg is one message read back from the kernel. payload is everything after the netlink
// header.
type nlmsg struct {
	typ, flags uint16
	seq        uint32
	payload    []byte
}

func parseMessages(b []byte) ([]nlmsg, error) {
	var out []nlmsg
	for len(b) >= nlmsgHdrLen {
		n := int(binary.NativeEndian.Uint32(b))
		if n < nlmsgHdrLen || n > len(b) {
			return nil, fmt.Errorf("linkguard: netlink message of %d bytes in a read of %d", n, len(b))
		}
		out = append(out, nlmsg{
			typ:     binary.NativeEndian.Uint16(b[4:]),
			flags:   binary.NativeEndian.Uint16(b[6:]),
			seq:     binary.NativeEndian.Uint32(b[8:]),
			payload: b[nlmsgHdrLen:n],
		})
		b = b[min(align4(n), len(b)):]
	}
	return out, nil
}

// errno reads an NLMSG_ERROR: 0 is an acknowledgement.
func (m nlmsg) errno() int {
	if len(m.payload) < 4 {
		return 0
	}
	return -int(int32(binary.NativeEndian.Uint32(m.payload)))
}

// attrs returns the attributes of an nftables message, after its nfgenmsg.
func (m nlmsg) attrs() []byte {
	if len(m.payload) < nfgenLen {
		return nil
	}
	return m.payload[nfgenLen:]
}

type rawAttr struct {
	typ  uint16
	data []byte
}

func parseAttrs(b []byte) ([]rawAttr, error) {
	var out []rawAttr
	for len(b) >= 4 {
		n := int(binary.NativeEndian.Uint16(b))
		if n < 4 || n > len(b) {
			return nil, errors.New("linkguard: malformed netlink attribute")
		}
		out = append(out, rawAttr{typ: binary.NativeEndian.Uint16(b[2:]) & nlaTypeMask, data: b[4:n]})
		b = b[min(align4(n), len(b)):]
	}
	return out, nil
}

func findAttr(attrs []rawAttr, typ uint16) ([]byte, bool) {
	for _, a := range attrs {
		if a.typ == typ {
			return a.data, true
		}
	}
	return nil, false
}

// attrPath follows nested attributes by type and returns the innermost one's payload.
func attrPath(b []byte, path ...uint16) ([]byte, bool) {
	for _, typ := range path {
		attrs, err := parseAttrs(b)
		if err != nil {
			return nil, false
		}
		var ok bool
		if b, ok = findAttr(attrs, typ); !ok {
			return nil, false
		}
	}
	return b, true
}

func attrString(b []byte, typ uint16) string {
	v, _ := attrPath(b, typ)
	return string(bytes.TrimRight(v, "\x00"))
}

// matchAttrs reports whether got, a dumped object's attributes, has every attribute in want
// with the same value. The kernel dumps attributes it was not sent, such as handles, use
// counts and defaults, so the match is a subset at every level except a list.
func matchAttrs(want []attr, got []byte) bool {
	have, err := parseAttrs(got)
	if err != nil {
		return false
	}
	for _, w := range want {
		if w.sendOnly {
			continue
		}
		v, ok := findAttr(have, w.typ)
		if !ok {
			return false
		}
		switch {
		case w.list:
			if !matchList(w.nested, v) {
				return false
			}
		case w.nested != nil:
			if !matchAttrs(w.nested, v) {
				return false
			}
		default:
			if !bytes.Equal(w.data, v) {
				return false
			}
		}
	}
	return true
}

func matchList(want []attr, got []byte) bool {
	have, err := parseAttrs(got)
	if err != nil || len(have) != len(want) {
		return false
	}
	for i, w := range want {
		if have[i].typ != w.typ || !matchAttrs(w.nested, have[i].data) {
			return false
		}
	}
	return true
}

// ruleset is the table Install writes: one set of open gates, one map from slot address to
// mark and the three chains that consult them, as The kernel's own fence, 2 lays out.
type ruleset struct {
	table, set, gmap string
}

type chainSpec struct {
	name  string
	hook  uint32
	rules [][]attr
}

func (r ruleset) tableAttrs() []attr {
	return []attr{aStr(nftaTableName, r.table), aU32(nftaTableFlags, 0)}
}

func (r ruleset) sets() [][]attr {
	timeout := aU64(nftaSetTimeout, uint64(links.GuardTTL/time.Millisecond))
	return [][]attr{
		{
			aStr(nftaSetTable, r.table),
			aStr(nftaSetName, r.set),
			aU32(nftaSetFlags, nftSetTimeout),
			aU32(nftaSetKeyType, typeIPv4Addr<<concatTypeBits|typeMark),
			aU32(nftaSetKeyLen, 8),
			{typ: nftaSetID, data: binary.BigEndian.AppendUint32(nil, 1), sendOnly: true},
			timeout,
		},
		{
			aStr(nftaSetTable, r.table),
			aStr(nftaSetName, r.gmap),
			aU32(nftaSetFlags, nftSetMap|nftSetTimeout),
			aU32(nftaSetKeyType, typeIPv4Addr),
			aU32(nftaSetKeyLen, 4),
			aU32(nftaSetDataType, typeMark),
			aU32(nftaSetDataLen, 4),
			{typ: nftaSetID, data: binary.BigEndian.AppendUint32(nil, 2), sendOnly: true},
			timeout,
		},
	}
}

var (
	virtualNet = []byte{198, 18, 0, 0}
	virtualMsk = []byte{255, 254, 0, 0}
	slotNet    = []byte{10, 11, 0, 0}
	slotMsk    = []byte{255, 255, 0, 0}
)

const (
	offSaddr = 12
	offDaddr = 16
)

func (r ruleset) chains() []chainSpec {
	ipv4 := []attr{metaLoad(nftMetaNfproto, reg1), cmpEq(reg1, []byte{nfprotoIPv4})}
	inNet := func(off uint32, net, mask []byte) []attr {
		return []attr{payloadLoad(off, reg1), bitwise(reg1, mask), cmpEq(reg1, net)}
	}
	linkMark := func(load attr) []attr {
		return []attr{load, bitwise(reg1, hostU32(0xff000000)), cmpEq(reg1, hostU32(links.Mark(0)))}
	}
	rule := func(parts ...[]attr) []attr {
		var exprs []attr
		for _, p := range parts {
			exprs = append(exprs, p...)
		}
		return exprs
	}
	gateMiss := func(off uint32, mark attr) []attr {
		return []attr{payloadLoad(off, reg1), mark, lookup(r.set, reg1, true), drop()}
	}
	return []chainSpec{
		{name: "prerouting", hook: hookPrerouting, rules: [][]attr{
			rule(ipv4, inNet(offDaddr, virtualNet, virtualMsk),
				[]attr{payloadLoad(offSaddr, reg1), lookupMap(r.gmap, reg1, reg1), metaSet(nftMetaMark, reg1)}),
			rule(ipv4, inNet(offDaddr, virtualNet, virtualMsk), gateMiss(offSaddr, metaLoad(nftMetaMark, reg32x01))),
		}},
		{name: "output", hook: hookOutput, rules: [][]attr{
			rule(linkMark(metaLoad(nftMetaMark, reg1)), ipv4, inNet(offDaddr, slotNet, slotMsk),
				gateMiss(offDaddr, metaLoad(nftMetaMark, reg32x01))),
		}},
		{name: "input", hook: hookInput, rules: [][]attr{
			rule(ipv4, inNet(offSaddr, slotNet, slotMsk),
				[]attr{metaLoad(nftMetaL4proto, reg1), cmpEq(reg1, []byte{6})},
				linkMark(socketMark(reg1)), gateMiss(offSaddr, socketMark(reg32x01))),
		}},
	}
}

func (r ruleset) chainAttrs(c chainSpec) []attr {
	prio := int32(links.GuardPriority)
	return []attr{
		aStr(nftaChainTable, r.table),
		aStr(nftaChainName, c.name),
		aNest(nftaChainHook, aU32(nftaHookHooknum, c.hook), aU32(nftaHookPrio, uint32(prio))),
		aU32(nftaChainPolicy, nfAccept),
		aStr(nftaChainType, "filter"),
	}
}

func (r ruleset) ruleAttrs(chain string, exprs []attr) []attr {
	comment := append([]byte(ruleTag), 0)
	udata := append([]byte{udataRuleComment, byte(len(comment))}, comment...)
	return []attr{
		aStr(nftaRuleTable, r.table),
		aStr(nftaRuleChain, chain),
		{typ: nftaRuleExprs, nested: exprs, list: true},
		aBytes(nftaRuleUserdata, udata),
	}
}

// replace is the batch that swaps the table for a fresh one. Creating the table first, without
// NLM_F_EXCL, is what lets the delete succeed when there is no table yet. The kernel commits
// the whole batch or none of it.
func (r ruleset) replace() []message {
	name := aStr(nftaTableName, r.table)
	msgs := []message{
		{typ: nftMsgNewTable, flags: nlmFCreate, family: nfprotoINET, attrs: []attr{name}},
		{typ: nftMsgDelTable, family: nfprotoINET, attrs: []attr{name}},
		{typ: nftMsgNewTable, flags: nlmFCreate | nlmFExcl, family: nfprotoINET, attrs: r.tableAttrs()},
	}
	for _, s := range r.sets() {
		msgs = append(msgs, message{typ: nftMsgNewSet, flags: nlmFCreate | nlmFExcl, family: nfprotoINET, attrs: s})
	}
	chains := r.chains()
	for _, c := range chains {
		msgs = append(msgs, message{typ: nftMsgNewChain, flags: nlmFCreate | nlmFExcl, family: nfprotoINET, attrs: r.chainAttrs(c)})
	}
	for _, c := range chains {
		for _, exprs := range c.rules {
			msgs = append(msgs, message{typ: nftMsgNewRule, flags: nlmFCreate | nlmFAppend, family: nfprotoINET, attrs: r.ruleAttrs(c.name, exprs)})
		}
	}
	return msgs
}

// tableDump is what the kernel reports for one table: the attributes of the table, of each of
// its sets and chains, and of each rule, in the order the rule dump returned them.
type tableDump struct {
	table  []byte
	sets   [][]byte
	chains [][]byte
	rules  [][]byte
}

// matches reports whether d is exactly the table Install leaves: the table enabled, these two
// sets and three chains and nothing else, and each chain holding these rules and no others.
func (r ruleset) matches(d tableDump) bool {
	if !matchAttrs(r.tableAttrs(), d.table) {
		return false
	}
	want := r.sets()
	if len(d.sets) != len(want) || !matchEach(want, d.sets, nftaSetName) {
		return false
	}
	chains := r.chains()
	var chainAttrs [][]attr
	for _, c := range chains {
		chainAttrs = append(chainAttrs, r.chainAttrs(c))
	}
	if len(d.chains) != len(chains) || !matchEach(chainAttrs, d.chains, nftaChainName) {
		return false
	}
	byChain := map[string][][]byte{}
	for _, rule := range d.rules {
		chain := attrString(rule, nftaRuleChain)
		byChain[chain] = append(byChain[chain], rule)
	}
	total := 0
	for _, c := range chains {
		got := byChain[c.name]
		if len(got) != len(c.rules) {
			return false
		}
		for i, exprs := range c.rules {
			if !matchAttrs(r.ruleAttrs(c.name, exprs), got[i]) {
				return false
			}
		}
		total += len(got)
	}
	return total == len(d.rules)
}

// matchEach pairs each wanted object with the dumped one of the same name.
func matchEach(want [][]attr, got [][]byte, nameAttr uint16) bool {
	byName := map[string][]byte{}
	for _, g := range got {
		byName[attrString(g, nameAttr)] = g
	}
	for _, w := range want {
		var name string
		for _, a := range w {
			if a.typ == nameAttr {
				name = string(bytes.TrimRight(a.data, "\x00"))
			}
		}
		g, ok := byName[name]
		if !ok || !matchAttrs(w, g) {
			return false
		}
	}
	return true
}

func expr(name string, data ...attr) attr {
	return aNest(nftaListElem, aStr(nftaExprName, name), aNest(nftaExprData, data...))
}

func metaLoad(key, dreg uint32) attr {
	return expr("meta", aU32(nftaMetaKey, key), aU32(nftaMetaDreg, dreg))
}

func metaSet(key, sreg uint32) attr {
	return expr("meta", aU32(nftaMetaKey, key), aU32(nftaMetaSreg, sreg))
}

func payloadLoad(offset, dreg uint32) attr {
	return expr("payload",
		aU32(nftaPayloadDreg, dreg),
		aU32(nftaPayloadBase, nftPayloadNetwork),
		aU32(nftaPayloadOffset, offset),
		aU32(nftaPayloadLen, 4))
}

func bitwise(reg uint32, mask []byte) attr {
	return expr("bitwise",
		aU32(nftaBitwiseSreg, reg),
		aU32(nftaBitwiseDreg, reg),
		aU32(nftaBitwiseLen, uint32(len(mask))),
		aNest(nftaBitwiseMask, aBytes(nftaDataValue, mask)),
		aNest(nftaBitwiseXor, aBytes(nftaDataValue, make([]byte, len(mask)))))
}

func cmpEq(reg uint32, data []byte) attr {
	return expr("cmp", aU32(nftaCmpSreg, reg), aU32(nftaCmpOp, nftCmpEq), aNest(nftaCmpData, aBytes(nftaDataValue, data)))
}

func lookup(set string, sreg uint32, inv bool) attr {
	var flags uint32
	if inv {
		flags = nftLookupFInv
	}
	return expr("lookup", aStr(nftaLookupSet, set), aU32(nftaLookupSreg, sreg), aU32(nftaLookupFlags, flags))
}

func lookupMap(set string, sreg, dreg uint32) attr {
	return expr("lookup", aStr(nftaLookupSet, set), aU32(nftaLookupSreg, sreg), aU32(nftaLookupDreg, dreg), aU32(nftaLookupFlags, 0))
}

func socketMark(dreg uint32) attr {
	return expr("socket", aU32(nftaSocketKey, nftSocketMark), aU32(nftaSocketDreg, dreg))
}

func drop() attr {
	return expr("immediate",
		aU32(nftaImmediateDreg, regVerdict),
		aNest(nftaImmediateData, aNest(nftaDataVerdict, aU32(nftaVerdictCode, nfDrop))))
}

// hostU32 is a 32-bit register value as the kernel holds it, which is how marks are compared.
func hostU32(v uint32) []byte { return binary.NativeEndian.AppendUint32(nil, v) }

// elemOp is one element change in a batch.
type elemOp struct {
	del bool
	// excl makes an add fail with EEXIST when the element is already there.
	excl    bool
	set     string
	key     []byte
	data    []byte
	timeout time.Duration
}

func elemMessage(table string, op elemOp) message {
	elem := []attr{aNest(nftaSetElemKey, aBytes(nftaDataValue, op.key))}
	typ, flags := uint16(nftMsgDelSetElem), uint16(0)
	if !op.del {
		typ, flags = nftMsgNewSetElem, nlmFCreate
		if op.excl {
			flags |= nlmFExcl
		}
		if op.data != nil {
			elem = append(elem, aNest(nftaSetElemData, aBytes(nftaDataValue, op.data)))
		}
		if op.timeout > 0 {
			elem = append(elem, aU64(nftaSetElemTimeout, uint64(op.timeout/time.Millisecond)))
		}
	}
	return message{typ: typ, flags: flags, family: nfprotoINET, attrs: []attr{
		aStr(nftaSetElemListTable, table),
		aStr(nftaSetElemListSet, op.set),
		aNest(nftaSetElemListElements, aNest(nftaListElem, elem...)),
	}}
}

// element is one set element or map entry as dumped. data is nil for a set.
type element struct {
	key, data []byte
}

// parseElements reads the elements out of a NEWSETELEM message's attributes.
func parseElements(b []byte) ([]element, error) {
	list, ok := attrPath(b, nftaSetElemListElements)
	if !ok {
		return nil, nil
	}
	elems, err := parseAttrs(list)
	if err != nil {
		return nil, err
	}
	var out []element
	for _, e := range elems {
		if e.typ != nftaListElem {
			continue
		}
		key, ok := attrPath(e.data, nftaSetElemKey, nftaDataValue)
		if !ok {
			return nil, errors.New("linkguard: set element without a key")
		}
		data, _ := attrPath(e.data, nftaSetElemData, nftaDataValue)
		out = append(out, element{key: bytes.Clone(key), data: bytes.Clone(data)})
	}
	return out, nil
}
