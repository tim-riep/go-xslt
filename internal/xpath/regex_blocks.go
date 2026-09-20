package xpath

import "fmt"

// xsdBlockRanges maps XSD/Unicode block names (the part after "Is" in
// \p{IsBlockName}) to their codepoint range, used to translate XSD regex block
// escapes — which Go's RE2 does not understand — into explicit ranges. Surrogate
// blocks map to the never-matching sentinel (no valid scalar value is a surrogate).
var xsdBlockRanges = map[string][2]rune{
	"BasicLatin":                           {0x0000, 0x007F},
	"Latin-1Supplement":                    {0x0080, 0x00FF},
	"LatinExtended-A":                      {0x0100, 0x017F},
	"LatinExtended-B":                      {0x0180, 0x024F},
	"IPAExtensions":                        {0x0250, 0x02AF},
	"SpacingModifierLetters":               {0x02B0, 0x02FF},
	"CombiningDiacriticalMarks":            {0x0300, 0x036F},
	"GreekandCoptic":                       {0x0370, 0x03FF},
	"Greek":                                {0x0370, 0x03FF},
	"Cyrillic":                             {0x0400, 0x04FF},
	"Armenian":                             {0x0530, 0x058F},
	"Hebrew":                               {0x0590, 0x05FF},
	"Arabic":                               {0x0600, 0x06FF},
	"Syriac":                               {0x0700, 0x074F},
	"Thaana":                               {0x0780, 0x07BF},
	"Devanagari":                           {0x0900, 0x097F},
	"Bengali":                              {0x0980, 0x09FF},
	"Gurmukhi":                             {0x0A00, 0x0A7F},
	"Gujarati":                             {0x0A80, 0x0AFF},
	"Oriya":                                {0x0B00, 0x0B7F},
	"Tamil":                                {0x0B80, 0x0BFF},
	"Telugu":                               {0x0C00, 0x0C7F},
	"Kannada":                              {0x0C80, 0x0CFF},
	"Malayalam":                            {0x0D00, 0x0D7F},
	"Sinhala":                              {0x0D80, 0x0DFF},
	"Thai":                                 {0x0E00, 0x0E7F},
	"Lao":                                  {0x0E80, 0x0EFF},
	"Tibetan":                              {0x0F00, 0x0FFF},
	"Myanmar":                              {0x1000, 0x109F},
	"Georgian":                             {0x10A0, 0x10FF},
	"HangulJamo":                           {0x1100, 0x11FF},
	"Ethiopic":                             {0x1200, 0x137F},
	"Cherokee":                             {0x13A0, 0x13FF},
	"UnifiedCanadianAboriginalSyllabics":   {0x1400, 0x167F},
	"Ogham":                                {0x1680, 0x169F},
	"Runic":                                {0x16A0, 0x16FF},
	"Khmer":                                {0x1780, 0x17FF},
	"Mongolian":                            {0x1800, 0x18AF},
	"LatinExtendedAdditional":              {0x1E00, 0x1EFF},
	"GreekExtended":                        {0x1F00, 0x1FFF},
	"GeneralPunctuation":                   {0x2000, 0x206F},
	"SuperscriptsandSubscripts":            {0x2070, 0x209F},
	"CurrencySymbols":                      {0x20A0, 0x20CF},
	"CombiningDiacriticalMarksforSymbols":  {0x20D0, 0x20FF},
	"CombiningMarksforSymbols":             {0x20D0, 0x20FF},
	"LetterlikeSymbols":                    {0x2100, 0x214F},
	"NumberForms":                          {0x2150, 0x218F},
	"Arrows":                               {0x2190, 0x21FF},
	"MathematicalOperators":                {0x2200, 0x22FF},
	"MiscellaneousTechnical":               {0x2300, 0x23FF},
	"ControlPictures":                      {0x2400, 0x243F},
	"OpticalCharacterRecognition":          {0x2440, 0x245F},
	"EnclosedAlphanumerics":                {0x2460, 0x24FF},
	"BoxDrawing":                           {0x2500, 0x257F},
	"BlockElements":                        {0x2580, 0x259F},
	"GeometricShapes":                      {0x25A0, 0x25FF},
	"MiscellaneousSymbols":                 {0x2600, 0x26FF},
	"Dingbats":                             {0x2700, 0x27BF},
	"BraillePatterns":                      {0x2800, 0x28FF},
	"CJKRadicalsSupplement":                {0x2E80, 0x2EFF},
	"KangxiRadicals":                       {0x2F00, 0x2FDF},
	"IdeographicDescriptionCharacters":     {0x2FF0, 0x2FFF},
	"CJKSymbolsandPunctuation":             {0x3000, 0x303F},
	"Hiragana":                             {0x3040, 0x309F},
	"Katakana":                             {0x30A0, 0x30FF},
	"Bopomofo":                             {0x3100, 0x312F},
	"HangulCompatibilityJamo":              {0x3130, 0x318F},
	"Kanbun":                               {0x3190, 0x319F},
	"BopomofoExtended":                     {0x31A0, 0x31BF},
	"EnclosedCJKLettersandMonths":          {0x3200, 0x32FF},
	"CJKCompatibility":                     {0x3300, 0x33FF},
	"CJKUnifiedIdeographsExtensionA":       {0x3400, 0x4DBF},
	"CJKUnifiedIdeographs":                 {0x4E00, 0x9FFF},
	"YiSyllables":                          {0xA000, 0xA48F},
	"YiRadicals":                           {0xA490, 0xA4CF},
	"HangulSyllables":                      {0xAC00, 0xD7A3},
	"HighSurrogates":                       {-1, -1},
	"HighPrivateUseSurrogates":             {-1, -1},
	"LowSurrogates":                        {-1, -1},
	"PrivateUseArea":                       {0xE000, 0xF8FF},
	"PrivateUse":                           {0xE000, 0xF8FF},
	"CJKCompatibilityIdeographs":           {0xF900, 0xFAFF},
	"AlphabeticPresentationForms":          {0xFB00, 0xFB4F},
	"ArabicPresentationForms-A":            {0xFB50, 0xFDFF},
	"CombiningHalfMarks":                   {0xFE20, 0xFE2F},
	"CJKCompatibilityForms":                {0xFE30, 0xFE4F},
	"SmallFormVariants":                    {0xFE50, 0xFE6F},
	"ArabicPresentationForms-B":            {0xFE70, 0xFEFF},
	"HalfwidthandFullwidthForms":           {0xFF00, 0xFFEF},
	"Specials":                             {0xFFF0, 0xFFFD},
	"OldItalic":                            {0x10300, 0x1032F},
	"Gothic":                               {0x10330, 0x1034F},
	"Deseret":                              {0x10400, 0x1044F},
	"ByzantineMusicalSymbols":              {0x1D000, 0x1D0FF},
	"MusicalSymbols":                       {0x1D100, 0x1D1FF},
	"MathematicalAlphanumericSymbols":      {0x1D400, 0x1D7FF},
	"Emoticons":                            {0x1F600, 0x1F64F},
	"CJKUnifiedIdeographsExtensionB":       {0x20000, 0x2A6DF},
	"CJKCompatibilityIdeographsSupplement": {0x2F800, 0x2FA1F},
	"Tags":                                 {0xE0000, 0xE007F},
	"SupplementaryPrivateUseArea-A":        {0xF0000, 0xFFFFD},
	"SupplementaryPrivateUseArea-B":        {0x100000, 0x10FFFD},
}

// xsdBlockClassBody returns the body of a character class (without brackets)
// matching the named XSD Unicode block, e.g. "BasicLatin" -> `\x{0}-\x{7f}`.
// Surrogate blocks return ("", true): they contain no valid scalar value, so the
// class body is empty (matches nothing in a positive class).
// XSDBlockKnown reports whether name (without the "Is" prefix) is a Unicode
// block this engine knows — XSD 1.0 rejects unknown blocks while 1.1 treats
// them as matching every character (reK88, bug 13670).
func XSDBlockKnown(name string) bool {
	_, ok := xsdBlockClassBody(name)
	return ok
}

func xsdBlockClassBody(name string) (string, bool) {
	r, ok := xsdBlockRanges[name]
	if !ok {
		return "", false
	}
	if r[0] < 0 { // surrogate block — no valid scalar values
		return "", true
	}
	return fmt.Sprintf(`\x{%x}-\x{%x}`, r[0], r[1]), true
}

// xsdBlockComplementBody renders the codepoints OUTSIDE a Unicode block as a
// character-class body. \P{IsBlock} inside an enclosing class cannot use RE2's
// \P negation, so the complement is emitted as explicit ranges instead.
func xsdBlockComplementBody(name string) (string, bool) {
	r, ok := xsdBlockRanges[name]
	if !ok {
		return "", false
	}
	if r[0] < 0 { // surrogate block — every scalar value is outside it
		return emitRanges([][2]rune{{0, 0x10ffff}}), true
	}
	var rr [][2]rune
	if r[0] > 0 {
		rr = append(rr, [2]rune{0, r[0] - 1})
	}
	if r[1] < 0x10ffff {
		rr = append(rr, [2]rune{r[1] + 1, 0x10ffff})
	}
	return emitRanges(rr), true
}

// blockClass renders a translated block range as RE2 syntax, honouring negation
// (\P) and whether it appears inside an enclosing character class. A surrogate
// block has an empty body: positive → match nothing, negative → match anything.
func blockClass(body string, neg, inClass bool) string {
	const full = `\x{0}-\x{10ffff}`
	if inClass {
		return body // \P inside a class is not expressible; emit the positive body
	}
	if body == "" { // surrogate block
		if neg {
			return "[" + full + "]"
		}
		return "[^" + full + "]"
	}
	if neg {
		return "[^" + body + "]"
	}
	return "[" + body + "]"
}
