package camundaapi

// ColourEnum is a widget colour.
type ColourEnum string

// List of ColourEnum
const (
	COLOURENUM_RED  ColourEnum = "RED"
	COLOURENUM_BLUE ColourEnum = "BLUE"
)

// All allowed values of ColourEnum enum
var AllowedColourEnumEnumValues = []ColourEnum{"RED", "BLUE"}

// Shapes and sizes of widgets.
type (
	// Shape is grouped in a parenthesized declaration, so its doc is on the spec.
	Shape string
	Size  int
)

// NewWidget instantiates a new Widget.
func NewWidget(name string, tags ...string) *Widget { return &Widget{Name: name} }

func ParseWidget(string, bool) (Widget, error) { return Widget{}, nil }

func Touch(w *Widget) {}

func unexportedHelper() {}
