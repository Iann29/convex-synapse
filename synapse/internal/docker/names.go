package docker

import (
	"crypto/rand"
	"encoding/binary"
)

// Lists picked for compactness, kid-friendliness, and avoiding common slurs/
// trademark conflicts. Mirrors the cloud convention of "adjective-animal-N".
var adjectives = []string{
	"quiet", "fast", "bright", "fuzzy", "calm", "bold", "swift", "cosmic",
	"witty", "lucky", "snappy", "merry", "lush", "brave", "neat", "sunny",
	"mellow", "sturdy", "perky", "agile", "patient", "proud", "amber",
	"quirky", "humble", "spicy", "tidy", "vivid", "wise", "zesty", "feral",
	"jolly", "loyal", "sharp", "nimble",
}

var animals = []string{
	"cat", "rabbit", "fox", "otter", "panda", "lemur", "tiger", "lynx",
	"finch", "hawk", "moth", "bee", "cricket", "wolf", "bear", "moose",
	"raccoon", "badger", "weasel", "ferret", "stoat", "marmot", "skunk",
	"penguin", "seal", "whale", "dolphin", "axolotl", "newt", "frog",
	"capybara", "alpaca", "ibis", "heron", "owl", "kestrel",
}

// GenerateDeploymentName returns a friendly, globally-unique-ish name like
// "quiet-cat-1234". Callers must still check the database for a collision —
// at scale the 4-digit suffix has plenty of room but is not infallible.
func GenerateDeploymentName() string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	num := binary.BigEndian.Uint16(buf[:2])%9000 + 1000
	adj := adjectives[uint(buf[2])%uint(len(adjectives))]
	ani := animals[uint(buf[3])%uint(len(animals))]
	return adj + "-" + ani + "-" + itoa4(int(num))
}

func itoa4(n int) string {
	out := []byte{'0', '0', '0', '0'}
	for i := 3; i >= 0 && n > 0; i-- {
		out[i] = byte('0' + n%10)
		n /= 10
	}
	return string(out)
}
