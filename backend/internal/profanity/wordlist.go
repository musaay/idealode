package profanity

// profaneWordsTR, Türkçe küfür/ağır hakaret sözlüğü — normalize()'ın
// üreteceği biçimle eşleşsin diye ÖNCEDEN katlanmış yazılır (ş->s, ğ->g,
// ç->c, ö->o, ü->u). 'ı' KATLANMAZ (reviewer bulgusu, #100) — doğru Türkçe
// yazımda geçen 'ı' harfi buraya aynen yazılır (ör. "amcık", "gerizekalı");
// wordPattern bu harfi metinde hem 'ı' hem 'i' ile eşleştirir, düz 'i' ise
// yalnız 'i' ile eşleşir ("sık" gibi masum kelimeler bu sayede yakalanmaz).
// Bu dosya kasıtlı olarak ayrı tutulur (#100 spec): repo PUBLIC, içerik
// kaçınılmaz olarak rahatsız edici kelimeler barındırır.
var profaneWordsTR = []string{
	"sik",
	"siktir",
	"yarrak",
	"orospu",
	"ibne",
	"amcık",
	"kahpe",
	"pust",
	"surtuk",
	"yavsak",
	"salak",
	"gerizekalı",
	"ahmak",
	"aptal",
	"dangalak",
}

// profaneWordsEN, İngilizce küfür/ağır hakaret sözlüğü (Türkçe katlama
// gerektirmez, ASCII). "dick" ve "got"/"piç" gibi çok yaygın masum
// kelimelerle (isim/fiil) çakışan girişler kasıtlı olarak DIŞARIDA
// bırakılmıştır — yanlış pozitif riski gerçek küfür yakalama kazancından
// yüksek (bkz. #100 yorumundaki Essex/assess örneği ile aynı ilke).
var profaneWordsEN = []string{
	"fuck",
	"shit",
	"bitch",
	"asshole",
	"bastard",
	"cunt",
	"motherfucker",
	"dumbass",
	"jackass",
	"idiot",
	"moron",
	"ass",
}
