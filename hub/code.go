package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Pairing codes look like TULIP-ANCHOR-42: two words from a fixed 256-word list
// plus two digits (~22.6 bits). That is deliberately small — a human types it
// once — and safe only because the PAKE limits an attacker to online guesses,
// which the Hub counts (maxCodeFailures) and throttles.
//
// The word list favours short, concrete, unambiguous English nouns that are
// easy to read aloud over the phone and hard to mishear. It must never change
// order or content once shipped: codes are not versioned.

const (
	codeWords       = 2
	codeDigits      = 2
	maxCodeFailures = 5
)

var wordList = [256]string{
	"acorn", "actor", "album", "alpha", "amber", "anchor", "angel", "ankle",
	"apple", "apron", "arrow", "atlas", "attic", "badge", "bagel", "baker",
	"bamboo", "banjo", "barn", "basil", "basket", "beach", "bean", "bear",
	"beetle", "bell", "berry", "bird", "biscuit", "blanket", "bloom", "boat",
	"bonus", "book", "boot", "bottle", "bread", "brick", "bridge", "broom",
	"brush", "bucket", "bulb", "butter", "button", "cabin", "cable", "cactus",
	"camel", "camera", "candle", "canoe", "canvas", "carpet", "carrot", "castle",
	"cedar", "cello", "chair", "chalk", "cherry", "chess", "chimney", "cider",
	"circle", "cliff", "clock", "cloud", "clover", "coal", "cobra", "cocoa",
	"coffee", "comet", "compass", "copper", "coral", "cotton", "crab", "crane",
	"crayon", "cricket", "crown", "crystal", "cube", "daisy", "dancer", "deer",
	"desk", "diamond", "dice", "dinner", "dolphin", "donkey", "dragon", "drum",
	"duck", "eagle", "earth", "echo", "elbow", "engine", "envelope", "falcon",
	"feather", "fence", "fern", "ferry", "fiddle", "finch", "flag", "flute",
	"forest", "frog", "garden", "garlic", "gecko", "giant", "ginger", "glacier",
	"glove", "goat", "goose", "grape", "guitar", "hammer", "harbor", "harp",
	"hawk", "hazel", "heron", "hill", "honey", "horse", "hotel", "igloo",
	"iron", "island", "ivory", "jacket", "jaguar", "jelly", "jewel", "juice",
	"jungle", "kayak", "kettle", "kiwi", "koala", "ladder", "lake", "lamp",
	"lantern", "laser", "leaf", "lemon", "lily", "lion", "lizard", "llama",
	"lobster", "locket", "magnet", "mango", "maple", "marble", "meadow", "melon",
	"meteor", "mirror", "mitten", "monkey", "moon", "moose", "muffin", "mushroom",
	"napkin", "needle", "nest", "noodle", "ocean", "olive", "onion", "orange",
	"orbit", "otter", "oyster", "paddle", "palm", "panda", "paper", "parrot",
	"peach", "pearl", "pebble", "pencil", "penguin", "pepper", "piano", "pickle",
	"pillow", "pilot", "pine", "pirate", "pizza", "planet", "plum", "pocket",
	"poppy", "potato", "pumpkin", "puppy", "puzzle", "quilt", "rabbit", "radio",
	"rain", "raven", "ribbon", "river", "robot", "rocket", "rose", "ruby",
	"saddle", "salmon", "scarf", "shark", "sheep", "shell", "silver", "sled",
	"snail", "spoon", "squid", "star", "stone", "sugar", "swan", "table",
	"teapot", "tiger", "tomato", "tulip", "turtle", "velvet", "violin", "wagon",
	"walrus", "water", "whale", "wheat", "willow", "window", "wolf", "zebra",
}

var errInvalidCode = errors.New("invalid pairing code")

// generateCode returns a fresh code in its canonical display form.
func generateCode() (string, error) {
	parts := make([]string, 0, codeWords+1)
	for range codeWords {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(wordList))))
		if err != nil {
			return "", err
		}
		parts = append(parts, strings.ToUpper(wordList[n.Int64()]))
	}
	n, err := rand.Int(rand.Reader, big.NewInt(100))
	if err != nil {
		return "", err
	}
	parts = append(parts, fmt.Sprintf("%0*d", codeDigits, n.Int64()))
	return strings.Join(parts, "-"), nil
}

// normalizeCode turns whatever a human typed (lower case, spaces, extra
// dashes, missing leading zero) into the canonical form the Hub hashed, or
// reports that it cannot be a valid code at all. Only canonical bytes enter the
// PAKE so both sides derive the same password scalar.
func normalizeCode(raw string) (string, error) {
	fields := strings.FieldsFunc(strings.ToUpper(strings.TrimSpace(raw)), func(r rune) bool {
		return r == '-' || r == ' ' || r == '_' || r == '.' || r == ','
	})
	if len(fields) != codeWords+1 {
		return "", errInvalidCode
	}
	for _, w := range fields[:codeWords] {
		if !knownWord(w) {
			return "", errInvalidCode
		}
	}
	digits := fields[codeWords]
	if len(digits) == 0 || len(digits) > codeDigits {
		return "", errInvalidCode
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", errInvalidCode
		}
	}
	for len(digits) < codeDigits {
		digits = "0" + digits
	}
	return strings.Join(append(fields[:codeWords:codeWords], digits), "-"), nil
}

func knownWord(upper string) bool {
	for _, w := range wordList {
		if strings.EqualFold(w, upper) {
			return true
		}
	}
	return false
}
