package codexcert

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestReproducibleMachOHashIgnoresDerivedIdentityOnly(t *testing.T) {
	one := machOFixture(1, 2, 3)
	two := machOFixture(9, 8, 3)
	three := machOFixture(1, 2, 4)
	canonicalOne, canonicalTwo := append([]byte(nil), one...), append([]byte(nil), two...)
	if err := canonicalizeMachO(canonicalOne); err != nil {
		t.Fatal(err)
	}
	if err := canonicalizeMachO(canonicalTwo); err != nil {
		t.Fatal(err)
	}
	for index := range canonicalOne {
		if canonicalOne[index] != canonicalTwo[index] {
			t.Fatalf("derived data remains at byte %d: %d != %d", index, canonicalOne[index], canonicalTwo[index])
		}
	}
	write := func(name string, data []byte) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	h1, err := ReproducibleFileSHA256(write("one", one))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := ReproducibleFileSHA256(write("two", two))
	if err != nil {
		t.Fatal(err)
	}
	h3, err := ReproducibleFileSHA256(write("three", three))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("derived Mach-O UUID or signature changed reproducible hash")
	}
	if h1 == h3 {
		t.Fatal("executable content change did not change reproducible hash")
	}
	policy := append([]byte(nil), one...)
	binary.BigEndian.PutUint32(policy[128:132], csAdHoc|0x10000)
	h4, err := ReproducibleFileSHA256(write("policy", policy))
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h4 {
		t.Fatal("signature policy change did not change reproducible hash")
	}
	special := append([]byte(nil), one...)
	binary.BigEndian.PutUint32(special[140:144], 1)
	if _, err := ReproducibleFileSHA256(write("special", special)); err == nil {
		t.Fatal("signature with a special slot was accepted")
	}
	badOffset := append([]byte(nil), one...)
	binary.BigEndian.PutUint32(badOffset[132:136], 81)
	binary.BigEndian.PutUint32(badOffset[144:148], 0)
	if _, err := ReproducibleFileSHA256(write("bad-offset", badOffset)); err == nil {
		t.Fatal("out-of-range code hash offset was accepted")
	}
}

func machOFixture(uuid, signature, content byte) []byte {
	data := make([]byte, 196)
	binary.LittleEndian.PutUint32(data[0:4], machO64Little)
	binary.LittleEndian.PutUint32(data[16:20], 2)
	binary.LittleEndian.PutUint32(data[20:24], 40)
	binary.LittleEndian.PutUint32(data[32:36], lcUUID)
	binary.LittleEndian.PutUint32(data[36:40], 24)
	for i := 40; i < 56; i++ {
		data[i] = uuid
	}
	binary.LittleEndian.PutUint32(data[56:60], lcCodeSignature)
	binary.LittleEndian.PutUint32(data[60:64], 16)
	binary.LittleEndian.PutUint32(data[64:68], 96)
	binary.LittleEndian.PutUint32(data[68:72], 100)
	for i := 72; i < 96; i++ {
		data[i] = content
	}
	binary.BigEndian.PutUint32(data[96:100], csEmbedded)
	binary.BigEndian.PutUint32(data[100:104], 100)
	binary.BigEndian.PutUint32(data[104:108], 1)
	binary.BigEndian.PutUint32(data[108:112], 0)
	binary.BigEndian.PutUint32(data[112:116], 20)
	binary.BigEndian.PutUint32(data[116:120], csCodeDirectory)
	binary.BigEndian.PutUint32(data[120:124], 80)
	binary.BigEndian.PutUint32(data[124:128], 0x20100)
	binary.BigEndian.PutUint32(data[128:132], csAdHoc)
	binary.BigEndian.PutUint32(data[132:136], 48)
	binary.BigEndian.PutUint32(data[136:140], 44)
	binary.BigEndian.PutUint32(data[140:144], 0)
	binary.BigEndian.PutUint32(data[144:148], 1)
	binary.BigEndian.PutUint32(data[148:152], 96)
	data[152], data[153], data[155] = 32, 2, 12
	copy(data[160:164], "app\x00")
	for i := 164; i < 196; i++ {
		data[i] = signature
	}
	return data
}
