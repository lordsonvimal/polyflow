package toolchain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultDatastarVocab_V1AcceptsColon(t *testing.T) {
	v := DefaultDatastarVocab("datastar-v1")
	assert.True(t, v.IsDataOnKey("data-on:click"), "v1 must accept colon event syntax")
	assert.False(t, v.IsDataOnKey("data-on-click"), "v1 must not accept hyphen event syntax")
}

func TestDefaultDatastarVocab_V0AcceptsHyphen(t *testing.T) {
	v := DefaultDatastarVocab("datastar-v0")
	assert.True(t, v.IsDataOnKey("data-on-click"), "v0 must accept hyphen event syntax")
	assert.False(t, v.IsDataOnKey("data-on:click"), "v0 must not accept colon event syntax")
}

func TestDefaultDatastarVocab_FallbackAcceptsBoth(t *testing.T) {
	v := DefaultDatastarVocab("")
	assert.True(t, v.IsDataOnKey("data-on:click"), "fallback must accept colon event syntax")
	assert.True(t, v.IsDataOnKey("data-on-click"), "fallback must accept hyphen event syntax")
}

func TestDefaultDatastarVocab_UnknownVariantFallsThroughToCombined(t *testing.T) {
	v := DefaultDatastarVocab("datastar-future-99")
	assert.True(t, v.IsDataOnKey("data-on:click"))
	assert.True(t, v.IsDataOnKey("data-on-click"))
}

func TestDefaultDatastarVocab_ReactiveV1(t *testing.T) {
	v := DefaultDatastarVocab("datastar-v1")
	assert.True(t, v.IsReactiveAttrKey("data-show"))
	assert.True(t, v.IsReactiveAttrKey("data-when"))
	assert.True(t, v.IsReactiveAttrKey("data-attr:disabled"), "v1 colon attr prefix")
	assert.True(t, v.IsReactiveAttrKey("data-attr-class"), "v1 keeps hyphen attr for compat")
	assert.True(t, v.IsReactiveAttrKey("data-class-active"))
	assert.True(t, v.IsReactiveAttrKey("data-computed-total"))
}

func TestDefaultDatastarVocab_ReactiveV0(t *testing.T) {
	v := DefaultDatastarVocab("datastar-v0")
	assert.True(t, v.IsReactiveAttrKey("data-show"))
	assert.True(t, v.IsReactiveAttrKey("data-when"))
	assert.True(t, v.IsReactiveAttrKey("data-attr-class"), "v0 hyphen attr prefix")
	assert.False(t, v.IsReactiveAttrKey("data-attr:disabled"), "v0 must not accept colon attr prefix")
	assert.True(t, v.IsReactiveAttrKey("data-class-active"))
}

func TestDefaultDatastarVocab_V0DoesNotContainColon(t *testing.T) {
	v := DefaultDatastarVocab("datastar-v0")
	for _, key := range []string{"data-on:click", "data-on:input", "data-on:submit"} {
		assert.False(t, v.IsDataOnKey(key), "v0 must not recognize %q", key)
	}
}

func TestDefaultDatastarVocab_V1DoesNotContainHyphenEvents(t *testing.T) {
	v := DefaultDatastarVocab("datastar-v1")
	for _, key := range []string{"data-on-click", "data-on-input", "data-on-submit"} {
		assert.False(t, v.IsDataOnKey(key), "v1 must not recognize %q", key)
	}
}

func TestDefaultRegistry_DatastarV0RowPresent(t *testing.T) {
	reg := DefaultRegistry()
	// datastar v0.x resolves without inferred fallback
	sel := reg.Select(ToolDatastar, "0.9.0")
	assert.False(t, sel.Inferred, "v0 datastar must be a real supported variant, not a fallback")
	assert.Equal(t, "datastar-v0", sel.Backend.RuleVariant)
}

func TestDefaultRegistry_DatastarV1SelectedForCurrentVersion(t *testing.T) {
	reg := DefaultRegistry()
	sel := reg.Select(ToolDatastar, "1.1.0")
	assert.False(t, sel.Inferred)
	assert.Equal(t, "datastar-v1", sel.Backend.RuleVariant)
}

func TestDefaultDatastarVocab_V1AcceptsDataInit(t *testing.T) {
	v := DefaultDatastarVocab("datastar-v1")
	if !v.IsDataOnKey("data-init") {
		t.Error("v1 vocab must accept data-init (action-on-mount idiom)")
	}
}

func TestDefaultDatastarVocab_V0DoesNotAcceptDataInit(t *testing.T) {
	v := DefaultDatastarVocab("datastar-v0")
	if v.IsDataOnKey("data-init") {
		t.Error("v0 vocab must not accept data-init (v1 idiom)")
	}
}
