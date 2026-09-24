package main

import "testing"

// TestEnvEnabled, BLEND_ENABLED ayrıştırma kuralını sabitler (#186): yalnız
// "true"/"1"/"yes" (büyük-küçük harf duyarsız, boşluk kırpılmış) açar;
// boş ve başka her değer KAPALI.
func TestEnvEnabled(t *testing.T) {
	cases := map[string]bool{
		"":         false,
		"true":     true,
		"TRUE":     true,
		"True":     true,
		" true ":   true,
		"1":        true,
		" 1\n":     true,
		"yes":      true,
		"YES":      true,
		"\tYes ":   true,
		"false":    false,
		"0":        false,
		"no":       false,
		"on":       false,
		"enabled":  false,
		"y":        false,
		"t":        false,
		"2":        false,
		"true1":    false,
		"yes!":     false,
		"tru e":    false,
		"   ":      false,
		"\"true\"": false,
	}
	for in, want := range cases {
		if got := envEnabled(in); got != want {
			t.Errorf("envEnabled(%q) = %v, beklenen %v", in, got, want)
		}
	}
}
