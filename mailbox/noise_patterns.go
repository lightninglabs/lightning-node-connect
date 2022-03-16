package mailbox

const (
	// XX is the name of the Noise XX Handshake pattern that uses SPAKE2
	// to mask the ephemeral key sent in the first act.
	XX = "XXeke+SPAKE2"

	// KK is the name of the Noise KK Handshake pattern.
	KK = "KK"
)

var (
	// XXPattern represents the XX Noise pattern using SPAKE2 to mask
	// the ephemeral key of the first message.
	// -> me
	// <- e, ee, s, es
	// -> s, se
	XXPattern = HandshakePattern{
		Name: XX,
		Pattern: []MessagePattern{
			{
				Tokens:    []Token{me},
				Initiator: true,
				ActNum:    act1,
			},
			{
				Tokens:    []Token{e, ee, s, es},
				Initiator: false,
				ActNum:    act2,
			},
			{
				Tokens:    []Token{s, se},
				Initiator: true,
				ActNum:    act3,
			},
		},
	}

	// KKPattern represents the KK Noise pattern in which both initiator
	// and responder start off knowing the static remote key of the other.
	// -> s
	// <- s
	// ...
	// <- e, es, ss
	// -> e, ee, se
	KKPattern = HandshakePattern{
		Name: KK,
		PreMessages: []MessagePattern{
			{
				Tokens:    []Token{s},
				Initiator: true,
			},
			{
				Tokens:    []Token{s},
				Initiator: false,
			},
		},
		Pattern: []MessagePattern{
			{
				Tokens:    []Token{e, es, ss},
				Initiator: true,
				ActNum:    act1,
			},
			{
				Tokens:    []Token{e, ee, se},
				Initiator: false,
				ActNum:    act2,
			},
		},
	}
)

// HandshakePattern represents the Noise pattern being used.
type HandshakePattern struct {
	PreMessages []MessagePattern
	Pattern     []MessagePattern
	Name        string
}

// A MessagePattern represents a Token or a stream of Tokens. MessagePatterns
// are used to make up a HandshakePattern.
type MessagePattern struct {
	Tokens    []Token
	Initiator bool
	ActNum    ActNum
}

// Token represents either a public key that should be transmitted or received
// from the handshake peer or a DH operation that should be performed. Tokens
// are used to create MessagePatterns.
type Token string

const (
	e  Token = "e"
	s  Token = "s"
	ee Token = "ee"
	es Token = "es"
	se Token = "se"
	ss Token = "ss"
	me Token = "me"
)

// ActNum is the index of a message pattern within a handshake pattern.
type ActNum uint8

const (
	act1 ActNum = 1
	act2 ActNum = 2
	act3 ActNum = 3
)
