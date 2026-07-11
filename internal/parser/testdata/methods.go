package testdata

// UserService is the receiver struct for the methods below.
type UserService struct{}

// Pointer receiver: the containment linker must resolve "*UserService" → "UserService".
func (s *UserService) Save() {}

// Value receiver.
func (s UserService) Load() {}

// A plain function must carry no receiver meta.
func Helper() {}
