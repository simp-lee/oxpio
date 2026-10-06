package asset

import "testing"

func TestValidateLocalSVGAcceptsInternalReferences(t *testing.T) {
	for _, source := range []string{
		"\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"UTF-8\"?><svg xmlns=\"http://www.w3.org/2000/svg\"><style>.shape{fill:url(#paint)}</style><path id=\"paint\"/></svg>",
		`<svg xmlns="http://www.w3.org/2000/svg"><use href="#shape"/></svg>`,
		`<svg><style>/* @import url("old.css"); */ .shape{fill:url(\23 gradient)} .label{content:"url(old.css) @import"}</style><path id="gradient"/></svg>`,
		`<svg><path fill="u\72l(\23 gradient)"/></svg>`,
		`<svg><animate attributeName="fill" from="red" to="url(#paint)" values="red;url(#other)"/><set attributeName="href" to="#shape"/></svg>`,
		`<svg><text aria-label='@import "guide" image-set("one") url(https://example.test/label)'>Label</text></svg>`,
		`<svg xmlns:meta="urn:example:metadata"><metadata meta:href="https://example.test/info" meta:style="url(https://example.test/info)"/><path meta:fill="url(https://example.test/info)"/><meta:style>@import "https://example.test/info"</meta:style></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><metadata><style xmlns="">@import "https://example.test/info"</style><path xmlns="" href="https://example.test/info" fill="url(https://example.test/info)"/></metadata></svg>`,
		`<svg xmlns:a="urn:one" xmlns:b="urn:two"><rect width="16" a:width="8" b:width="4"/></svg>`,
		" \n<!-- before --><svg><style><![CDATA[/* url(old.css) */ .shape { fill: url(\\23 gradient) }]]></style></svg><!-- after -->\n",
	} {
		if err := ValidateLocalSVG([]byte(source)); err != nil {
			t.Fatalf("ValidateLocalSVG(%q) error = %v", source, err)
		}
	}
}

func TestValidateLocalSVGRejectsScriptingConstructs(t *testing.T) {
	for _, source := range []string{
		`<svg><script>alert(1)</script></svg>`,
		`<svg><SCRIPT>alert(1)</SCRIPT></svg>`,
		`<svg><path onload="alert(1)"/></svg>`,
		`<svg><g OnMouseOver="alert(1)"/></svg>`,
		`<svg><animate attributeName="onload" to="alert(1)"/></svg>`,
		`<svg><set attributeName="OnClick" to="alert(1)"/></svg>`,
	} {
		if err := ValidateLocalSVG([]byte(source)); err == nil {
			t.Fatalf("ValidateLocalSVG(%q) error = nil", source)
		}
	}
}

func TestValidateLocalSVGRejectsDuplicateExpandedAttributes(t *testing.T) {
	for _, source := range []string{
		`<svg><rect width="16" width="8" height="5"/></svg>`,
		`<svg xmlns:a="urn:example" xmlns:b="urn:example"><rect a:width="16" b:width="8"/></svg>`,
		`<svg xmlns:a="urn:one" xmlns:a="urn:two"/>`,
	} {
		if err := ValidateLocalSVG([]byte(source)); err == nil {
			t.Fatalf("ValidateLocalSVG(%q) error = nil", source)
		}
	}
}

func TestValidateLocalSVGRejectsMalformedAndExternalReferences(t *testing.T) {
	for _, source := range []string{
		`<svg><image href="https://example.test/image.png"/></svg>`,
		`<svg><path fill="url(https://example.test/fill)"/></svg>`,
		`<svg><path fill="u\72l(https://example.test/fill)"/></svg>`,
		`<svg><path fill="u\72l(#paint bad)"/></svg>`,
		`<svg><style>@import url("https://example.test/style.css")</style></svg>`,
		`<svg><style>@im\70ort "#local"</style></svg>`,
		`<svg><style>.shape{background-image:image-set("https://example.test/image.png" 1x)}</style></svg>`,
		`<svg><style>.shape{background-image:image("https://example.test/image.png")}</style></svg>`,
		`<svg><style>.shape{fill:url(#paint}</style></svg>`,
		`<svg><animate attributeName="fill" values="red;url(https://example.test/paint.svg#p)"/></svg>`,
		`<svg><animate attributeName="background-image" to="url(https://example.test/image.png)"/></svg>`,
		`<svg><set attributeName="href" to="https://example.test/shape.svg#shape"/></svg>`,
		`<svg xmlns:r="http://www.w3.org/1999/xlink"><set attributeName="r:href" to="https://example.test/shape.svg#shape"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><img xmlns="http://www.w3.org/1999/xhtml" src="https://example.test/image.png"/></foreignObject></svg>`,
		`<svg xml:base="https://example.test/sprite.svg"><use href="#shape"/></svg>`,
		`<svg><g xml:base="https://example.test/sprite.svg"><use href="#shape"/></g></svg>`,
		`<svg><text>unfinished</svg>`,
		`<?xml-stylesheet href="style.css"?><svg/>`,
		`<?xml?><svg/>`,
		`<?xml standalone="maybe"?><svg/>`,
		`<?XML version="1.0"?><svg/>`,
		"<?xml \fversion=\"1.0\"?><svg/>",
		`<!bogus><svg/>`,
		`<svg/><!DOCTYPE svg>`,
		`<svg/><?xml version="1.0"?>`,
	} {
		if err := ValidateLocalSVG([]byte(source)); err == nil {
			t.Fatalf("ValidateLocalSVG(%q) error = nil", source)
		}
	}
}

func TestValidateLocalSVGRejectsInvalidDocumentStructure(t *testing.T) {
	for _, source := range []string{
		`<svg/><svg/>`,
		`<svg/><other/>`,
		`<meta:svg xmlns:meta="urn:example:metadata"/>`,
		`before<svg/>`,
		`<svg/>after`,
		`<SVG/>`,
		"\u00a0<svg/>",
		`<![CDATA[ ]]><svg/>`,
		`&#x20;<svg/>`,
		`<svg/>&#x20;`,
	} {
		if err := ValidateLocalSVG([]byte(source)); err == nil {
			t.Fatalf("ValidateLocalSVG(%q) error = nil", source)
		}
	}
}
