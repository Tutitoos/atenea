package codexcert

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
)

const (
	machO64Little    = 0xfeedfacf
	lcUUID           = 0x1b
	lcCodeSignature  = 0x1d
	machO64HeaderLen = 32
	csEmbedded       = 0xfade0cc0
	csCodeDirectory  = 0xfade0c02
	csAdHoc          = 0x2
)

// ReproducibleFileSHA256 hashes executable content while excluding the two
// derived Mach-O fields that Apple linkers may regenerate for identical code:
// LC_UUID and ad-hoc CodeDirectory page hashes. Signature policy metadata is
// preserved, and signatures with special slots are rejected. Other formats
// are hashed byte-for-byte.
func ReproducibleFileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) >= 4 && binary.LittleEndian.Uint32(data[:4]) == machO64Little {
		if err := canonicalizeMachO(data); err != nil {
			return "", err
		}
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalizeMachO(data []byte) error {
	if len(data) < machO64HeaderLen {
		return errors.New("truncated Mach-O header")
	}
	ncmds := int(binary.LittleEndian.Uint32(data[16:20]))
	commandBytes := int(binary.LittleEndian.Uint32(data[20:24]))
	if commandBytes < 0 || machO64HeaderLen+commandBytes > len(data) {
		return errors.New("invalid Mach-O load commands")
	}
	offset := machO64HeaderLen
	for range ncmds {
		if offset+8 > machO64HeaderLen+commandBytes {
			return errors.New("truncated Mach-O load command")
		}
		command := binary.LittleEndian.Uint32(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		if size < 8 || offset+size > machO64HeaderLen+commandBytes {
			return errors.New("invalid Mach-O load command size")
		}
		switch command {
		case lcUUID:
			if size != 24 {
				return errors.New("invalid Mach-O UUID command")
			}
			clear(data[offset+8 : offset+24])
		case lcCodeSignature:
			if size != 16 {
				return errors.New("invalid Mach-O code signature command")
			}
			start := int(binary.LittleEndian.Uint32(data[offset+8 : offset+12]))
			length := int(binary.LittleEndian.Uint32(data[offset+12 : offset+16]))
			if start < 0 || length < 0 || start > len(data) || length > len(data)-start {
				return errors.New("invalid Mach-O code signature range")
			}
			if err := canonicalizeAdHocSignature(data[start : start+length]); err != nil {
				return err
			}
		}
		offset += size
	}
	return nil
}

func canonicalizeAdHocSignature(signature []byte) error {
	if len(signature) < 20 || binary.BigEndian.Uint32(signature[:4]) != csEmbedded {
		return errors.New("unsupported Mach-O code signature container")
	}
	total := int(binary.BigEndian.Uint32(signature[4:8]))
	count := int(binary.BigEndian.Uint32(signature[8:12]))
	if total != len(signature) || count != 1 || binary.BigEndian.Uint32(signature[12:16]) != 0 {
		return errors.New("Mach-O signature is not a single code directory")
	}
	offset := int(binary.BigEndian.Uint32(signature[16:20]))
	if offset < 20 || offset+40 > total || binary.BigEndian.Uint32(signature[offset:offset+4]) != csCodeDirectory {
		return errors.New("invalid Mach-O code directory")
	}
	directoryLength := int(binary.BigEndian.Uint32(signature[offset+4 : offset+8]))
	if directoryLength < 40 || offset+directoryLength > total {
		return errors.New("invalid Mach-O code directory length")
	}
	flags := binary.BigEndian.Uint32(signature[offset+12 : offset+16])
	hashOffset := int(binary.BigEndian.Uint32(signature[offset+16 : offset+20]))
	specialSlots := int(binary.BigEndian.Uint32(signature[offset+24 : offset+28]))
	codeSlots := int(binary.BigEndian.Uint32(signature[offset+28 : offset+32]))
	hashSize := int(signature[offset+36])
	if flags&csAdHoc == 0 || specialSlots != 0 || hashSize == 0 || hashOffset < 40 || hashOffset > directoryLength || codeSlots > (directoryLength-hashOffset)/hashSize {
		return errors.New("Mach-O code directory is not a plain ad-hoc signature")
	}
	clear(signature[offset+hashOffset : offset+hashOffset+codeSlots*hashSize])
	return nil
}
