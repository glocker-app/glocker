package syncer

import "testing"

func TestChunkPayload(t *testing.T) {
	var p Payload
	p.Encrypted = true
	for i := 0; i < 3; i++ {
		p.Violations = append(p.Violations, violation{TS: int64(50 + i)})
	}
	for i := 0; i < 12; i++ {
		p.Usage = append(p.Usage, usageRow{TS: int64(100 + i)})
	}

	chunks := chunkPayload(p, 5) // 15 records / 5 => 3 chunks
	if len(chunks) != 3 {
		t.Fatalf("want 3 chunks, got %d", len(chunks))
	}
	total, gotUsage, gotViol := 0, 0, 0
	for _, c := range chunks {
		if !c.p.Encrypted {
			t.Error("chunk dropped the Encrypted flag")
		}
		if c.count > 5 {
			t.Errorf("chunk exceeds size: %d", c.count)
		}
		total += c.count
		gotUsage += len(c.p.Usage)
		gotViol += len(c.p.Violations)
	}
	if total != 15 || gotUsage != 12 || gotViol != 3 {
		t.Fatalf("records lost: total=%d usage=%d viol=%d", total, gotUsage, gotViol)
	}
	// Last chunk's cursor advance must be the largest usage TS it carried.
	last := chunks[len(chunks)-1]
	if last.perSrc["usage"] != 111 || last.maxTS != 111 {
		t.Errorf("last chunk usage cursor = %d / maxTS %d, want 111", last.perSrc["usage"], last.maxTS)
	}
}
