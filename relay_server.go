package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	controlPort         = "34000"
	dataConnTimeout     = 15 * time.Second
	identTimeout        = 5 * time.Second
	authResponseTimeout = 10 * time.Second // Timeout for host to respond to password verification
)

var (
	adjectives = []string{
		"Agile", "Amber", "Ancient", "Aqua", "Arctic", "Azure", "Bold", "Brave", "Bright", "Bronze",
		"Calm", "Clear", "Clever", "Cloud", "Cobalt", "Cool", "Coral", "Cosmic", "Crimson", "Crystal",
		"Daring", "Dark", "Dawn", "Deep", "Desert", "Dewy", "Diamond", "Dim", "Dusty", "Eager",
		"Early", "Earth", "Ebony", "Echo", "Elder", "Emerald", "Empty", "Evening", "Fading", "Fair",
		"Fancy", "Fast", "Fiery", "First", "Fleet", "Forest", "Free", "Fresh", "Frost", "Gentle",
		"Giant", "Glade", "Glass", "Gleam", "Global", "Golden", "Grand", "Grass", "Gray", "Green",
		"Happy", "Harbor", "Hasty", "Hazel", "Heart", "Heavy", "Hidden", "High", "Hill", "Holy",
		"Honey", "Honor", "Hush", "Icy", "Ideal", "Idle", "Indigo", "Inner", "Iron", "Island",
		"Ivory", "Jade", "Jolly", "Jungle", "Junior", "Keen", "Kind", "King", "Lagoon", "Lake",
		"Large", "Last", "Late", "Laurel", "Lazy", "Leafy", "Light", "Lilac", "Little", "Lively",
		"Lone", "Long", "Lost", "Loud", "Low", "Loyal", "Lucky", "Lunar", "Lush", "Magic",
		"Major", "Maple", "Marble", "Marsh", "Master", "Meadow", "Merry", "Metal", "Midday", "Might",
		"Mild", "Milky", "Mist", "Model", "Modern", "Moon", "Morning", "Moss", "Motor", "Mountain",
		"Murky", "Mystic", "Narrow", "Navy", "Near", "Nebula", "New", "Night", "Noble", "North",
		"Nova", "Oasis", "Ocean", "Old", "Olive", "Omega", "Onyx", "Open", "Orange", "Orchid",
		"Outer", "Pale", "Paper", "Pastel", "Peak", "Pearl", "Pepper", "Petal", "Phantom", "Pilot",
		"Pine", "Pink", "Plain", "Plum", "Polar", "Pond", "Proud", "Pure", "Purple", "Pyrite",
		"Quake", "Quartz", "Queen", "Quest", "Quiet", "Quick", "Quill", "Radiant", "Rainy", "Rapid",
		"Raven", "Regal", "Rich", "River", "Road", "Rocky", "Rogue", "Royal", "Ruby", "Rural",
		"Rusty", "Sacred", "Saffron", "Sage", "Sand", "Sandy", "Sapphire", "Satin", "Scarlet", "Secret",
		"Serene", "Shadow", "Shady", "Sharp", "Shimmer", "Shiny", "Shore", "Short", "Silent", "Silk",
		"Silver", "Sky", "Slate", "Slow", "Small", "Smooth", "Snowy", "Solar", "Solid", "Space",
		"Spark", "Spicy", "Spirit", "Spring", "Star", "Still", "Stone", "Storm", "Stray", "Stream",
		"Street", "Strong", "Sugar", "Summer", "Sun", "Sunny", "Super", "Surf", "Sweet", "Swift",
		"Terra", "Tidal", "Timber", "Tiny", "Topaz", "Trail", "Tranquil", "Tundra", "Twilight", "Twin",
		"Ultra", "Urban", "Valley", "Velvet", "Venom", "Verdant", "Vivid", "Void", "Volcano", "Wander",
		"Warm", "Water", "Wave", "West", "Whisper", "White", "Wide", "Wild", "Windy", "Winter",
		"Wise", "Witty", "Wood", "Young", "Zenith", "Zephyr", "Zesty",
	}
	nouns = []string{
		"Albatross", "Alligator", "Alpaca", "Anaconda", "Angler", "Ant", "Antelope", "Ape", "ArcticFox", "Armadillo",
		"Arrow", "Aspen", "Aster", "Aurora", "Avalanche", "Badger", "Balloon", "Bamboo", "Banyan", "Barracuda",
		"Basilisk", "Bass", "Bat", "Beacon", "Bear", "Beaver", "Bee", "Beetle", "Bell", "Bison",
		"Blizzard", "Bloom", "Blossom", "Boa", "Boar", "Boat", "Bobcat", "Boulder", "Breeze", "Bridge",
		"Brook", "Buffalo", "Bugle", "Bumblebee", "Butterfly", "Buzzard", "Cactus", "Caldera", "Camel", "Canary",
		"Candle", "Canyon", "Capybara", "Caracal", "Caravan", "Cardinal", "Caribou", "Cascade", "Castle", "Cat",
		"Caterpillar", "Catfish", "Cavern", "Cedar", "Centaur", "Chameleon", "Channel", "Cheetah", "Cherry", "Chestnut",
		"Chickadee", "Chime", "Chimpanzee", "Chipmunk", "Cicada", "Cinder", "Cipher", "Claw", "Cliff", "Clock",
		"Cloud", "Clover", "Cobra", "Cocoon", "Cod", "Comet", "Condor", "Cone", "Coral", "Cougar",
		"Cove", "Coyote", "Crab", "Crane", "Crater", "Creek", "Crest", "Cricket", "Crocodile", "Crow",
		"Crown", "Crystal", "Current", "Cypress", "Dagger", "Daisy", "Dandelion", "Dart", "Dawn", "Deer",
		"Delta", "Desert", "Dewdrop", "Diamond", "Dingo", "Dinosaur", "Dipper", "Dolphin", "Donkey", "Dove",
		"Dragon", "Dragonfly", "Dream", "Drift", "Drum", "Duck", "Dune", "Dust", "Eagle", "Earth",
		"Easel", "Echo", "Eel", "Egret", "Elder", "Elephant", "Elk", "Ember", "Emerald", "Envoy",
		"Ermine", "Falcon", "Fang", "Feather", "Feline", "Fennel", "Fern", "Ferret", "Finch", "Firefly",
		"Fish", "Flame", "Flamingo", "Fleet", "Flint", "Flower", "Flute", "Fog", "Forest", "Fossil",
		"Fox", "Fragment", "Fresco", "Frog", "Frost", "Gale", "Galleon", "Garden", "Garland", "Garnet",
		"Gazelle", "Gecko", "Gem", "Geode", "Gerbil", "Geyser", "Ghost", "Giant", "Gibbon", "Ginger",
		"Giraffe", "Glacier", "Glade", "Glimmer", "Glow", "Gnat", "Goat", "Goldfish", "Gopher", "Gorilla",
		"Goshawk", "Grail", "Granite", "Grape", "Grass", "Griffin", "Grove", "Gull", "Hail", "Hammer",
		"Hamster", "Harbor", "Hare", "Harp", "Harrier", "Hawk", "Hazel", "Haze", "Heart", "Heath",
		"Hedgehog", "Helm", "Hemlock", "Heron", "Hibiscus", "Highland", "Hill", "Hippo", "Hive", "Holly",
		"Horizon", "Horn", "Hornet", "Horse", "Hound", "Hummingbird", "Hurricane", "Hyacinth", "Hyena", "Ibex",
		"Ibis", "Iceberg", "Icon", "Iguana", "Impala", "Ink", "Insect", "Iris", "Island", "Ivory",
		"Jackal", "Jaguar", "Jasmine", "Jasper", "Jay", "Jellyfish", "Jerboa", "Jewel", "Jungle", "Juniper",
		"Kangaroo", "Kelp", "Kestrel", "Key", "Kingfisher", "Kite", "Kiwi", "Knight", "Koala", "Koi",
		"Kraken", "Lagoon", "Lake", "Lamb", "Lamp", "Lance", "Land", "Lantern", "Larch", "Lark",
		"Laurel", "Lava", "Leaf", "Ledger", "Leech", "Legend", "Lemming", "Lemon", "Lemur", "Leopard",
		"Liberty", "Lichen", "Light", "Lightning", "Lily", "Lime", "Lion", "Lizard", "Llama", "Lobster",
		"Locust", "Log", "Loon", "Lotus", "Locket", "Lynx", "Lyre", "Macaw", "Maelstrom", "Magma",
		"Magpie", "Mammoth", "Manatee", "Mandrake", "Mantis", "Maple", "Map", "Marble", "Marigold", "Marlin",
		"Marmot", "Marsh", "Mask", "Mastodon", "Meadow", "Medal", "Meerkat", "Melody", "Meteor", "Mill",
		"Mink", "Minnow", "Mint", "Mirage", "Mirror", "Mist", "Mockingbird", "Mole", "Monarch", "Mongoose",
		"Monkey", "Monolith", "Monsoon", "Moon", "Moose", "Morning", "Mosaic", "Mosquito", "Moss", "Moth",
		"Mountain", "Mouse", "Mule", "Murmur", "Mushroom", "Muskox", "Mustang", "Myrtle", "Naiad", "Narwhal",
		"Needle", "Nest", "Newt", "Night", "Nightingale", "Nimbus", "Nomad", "NorthStar", "Note", "Nova",
		"Nugget", "Nut", "Nuthatch", "Nymph", "Oak", "Oasis", "Oat", "Obsidian", "Ocean", "Ocelot",
		"Octopus", "Olive", "Omega", "Opal", "Oracle", "Orange", "Orb", "Orchid", "Orca", "Oriole",
		"Osprey", "Ostrich", "Otter", "Owl", "Ox", "Oyster", "Pagoda", "Palm", "Panther", "Papaya",
		"Paper", "Paradise", "Parasol", "Parchment", "Parrot", "Parsley", "Path", "Paw", "Peacock", "Peak",
		"Pearl", "Pegasus", "Pelican", "Pendant", "Penguin", "Pen", "Peony", "Pepper", "Peregrine", "Petal",
		"Petrel", "Phantom", "Pheasant", "Phoenix", "Pigeon", "Pike", "Pilot", "Pine", "Pinnacle", "Pioneer",
		"Pipe", "Piranha", "Pistol", "PitViper", "Planet", "Plankton", "Plateau", "Platypus", "Plaza", "Plume",
		"Pointer", "PolarBear", "Pollen", "Pond", "Pony", "Poppy", "Porcupine", "Portal", "Possum", "Prairie",
		"Primrose", "Prism", "Puffin", "Puma", "Pyramid", "Python", "Quail", "Quartz", "Quest", "Quill",
		"Rabbit", "Raccoon", "Racer", "Radiance", "Rainbow", "Ram", "Raptor", "Rat", "Rattlesnake", "Raven",
		"Ray", "Realm", "Reed", "Reef", "Reflection", "Reindeer", "Relic", "Rhino", "Rhythm", "Ridge",
		"Rifle", "Ring", "Ripple", "River", "Road", "Robin", "Rock", "Rocket", "Rodent", "Rook",
		"Rooster", "Root", "Rose", "Ruby", "Rune", "Saber", "Sable", "Sage", "Sail", "Salamander",
		"Salmon", "Sand", "Sandalwood", "Sandpiper", "Sapphire", "Sarcophagus", "Sardine", "Saturn", "Savanna", "Scale",
		"Scarab", "Scepter", "Scorpion", "Scout", "Scroll", "Scythe", "Sea", "Seagull", "Seahorse", "Seal",
		"Seed", "Serpent", "Shadow", "Shaft", "Shark", "Sheep", "Shell", "Shield", "Ship", "Shore",
		"Shrew", "Shrimp", "Shrine", "Sigil", "Silk", "Silver", "Skink", "Skunk", "Sky", "Skylark",
		"Slate", "Slipper", "Sloth", "Smoke", "Snail", "Snake", "Snipe", "Snow", "Snowflake", "SnowLeopard",
		"Soil", "Solstice", "Song", "Sorrel", "Soul", "Spark", "Sparrow", "Spear", "Sphinx", "Spider",
		"Spike", "Spire", "Spirit", "Spring", "Sprite", "Spruce", "Spur", "Spyglass", "Squall", "Squid",
		"Squirrel", "Staff", "Stag", "Stalactite", "Stallion", "Star", "Starfish", "Starling", "Statue", "Steam",
		"Steel", "Steeple", "Stick", "Stingray", "Stoat", "Stone", "Stork", "Storm", "Stream", "Street",
		"Summit", "Sun", "Sunbeam", "Sunflower", "Surf", "Swallow", "Swamp", "Swan", "Sword", "Sycamore",
		"Symbol", "Tadpole", "Talisman", "Talon", "Tapestry", "Tarot", "Tarsier", "Tea", "Tempest", "Temple",
		"Termite", "Thistle", "Thorn", "Thrush", "Thunder", "Tiger", "Timber", "Toad", "Token", "Tomb",
		"Topaz", "Torch", "Tornado", "Torpedo", "Tortoise", "Toucan", "Tower", "Trail", "Train", "Treasure",
		"Tree", "Trellis", "Triangle", "Trident", "Trillium", "Troll", "Trophy", "Trout", "Trumpet", "Trunk",
		"Tsunami", "Tuber", "Tulip", "Tundra", "Tunnel", "Turquoise", "Turtle", "Twilight", "Typhoon", "Umber",
		"Unicorn", "Urchin", "Valley", "Vanguard", "Vanilla", "Vapor", "Vault", "Veil", "Velvet", "Venom",
		"Vessel", "Vine", "Violet", "Viper", "Vista", "Voice", "Volcano", "Vortex", "Vulture", "Wallaby",
		"Walnut", "Walrus", "Wand", "Wanderer", "Warbler", "Wasp", "Watch", "Water", "Waterfall", "Wave",
		"Weasel", "Web", "Well", "Whale", "Wheat", "Whirlwind", "Whisper", "Willow", "Wind", "Wing",
		"Winter", "Wolf", "Wolverine", "Wood", "Woodpecker", "Worm", "Wren", "Xylophone", "Yak", "Yam",
		"Yarrow", "Yew", "Yeti", "Zebra", "Zenith", "Zephyr", "Zinnia", "Zircon", "Zodiac",
	}
)

// PendingAuthRequest stores information about a launcher waiting for host password verification.
type PendingAuthRequest struct {
	launcherConn  net.Conn
	targetHostID  string
	initiatedTime time.Time
}

// RelayServer manages the state of the relay.
type RelayServer struct {
	hostControlConns       map[string]net.Conn           // hostID -> control connection from host's sidecar
	mu                     sync.Mutex                    // Protects hostControlConns
	pendingAuthentications map[string]PendingAuthRequest // requestToken -> PendingAuthRequest
	authMu                 sync.Mutex                    // Protects pendingAuthentications
}

// NewRelayServer creates a new relay server instance.
func NewRelayServer() *RelayServer {
	rand.Seed(time.Now().UnixNano())
	return &RelayServer{
		hostControlConns:       make(map[string]net.Conn),
		pendingAuthentications: make(map[string]PendingAuthRequest),
	}
}

// generateMemorableID creates a unique, memorable ID for a host.
func (r *RelayServer) generateMemorableID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	maxAttempts := 10
	for attempt := 0; attempt < maxAttempts; attempt++ {
		adj := adjectives[rand.Intn(len(adjectives))]
		noun := nouns[rand.Intn(len(nouns))]
		id := adj + noun
		if _, exists := r.hostControlConns[id]; !exists {
			return id
		}
	}
	for i := 2; ; i++ {
		adj := adjectives[rand.Intn(len(adjectives))]
		noun := nouns[rand.Intn(len(nouns))]
		id := fmt.Sprintf("%s%s%d", adj, noun, i)
		if _, exists := r.hostControlConns[id]; !exists {
			return id
		}
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	relay := NewRelayServer()
	listener, err := net.Listen("tcp", ":"+controlPort)
	if err != nil {
		log.Fatalf("FATAL: Failed to listen on control port %s: %v", controlPort, err)
	}
	defer listener.Close()
	log.Printf("INFO: Relay server listening for control connections on port %s", controlPort)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("ERROR: Failed to accept control connection: %v", err)
			continue
		}
		go relay.handleControlConnection(conn)
	}
}

func (r *RelayServer) handleControlConnection(conn net.Conn) {
	defer conn.Close()
	remoteAddr := conn.RemoteAddr().String()
	log.Printf("INFO: New control connection from: %s", remoteAddr)
	reader := bufio.NewReader(conn)
	var registeredHostID string

	for {
		message, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("ERROR: Reading from control connection %s: %v", remoteAddr, err)
			} else {
				log.Printf("INFO: Control connection %s closed (EOF)", remoteAddr)
			}
			if registeredHostID != "" {
				r.mu.Lock()
				if currentConn, ok := r.hostControlConns[registeredHostID]; ok && currentConn == conn {
					log.Printf("INFO: Host '%s' (control conn %s) disconnected. Removing from registry.", registeredHostID, remoteAddr)
					delete(r.hostControlConns, registeredHostID)
				}
				r.mu.Unlock()
			}
			r.authMu.Lock()
			for token, pendingReq := range r.pendingAuthentications {
				if pendingReq.launcherConn == conn {
					log.Printf("INFO: Launcher connection %s for pending auth token %s disconnected. Removing pending auth.", remoteAddr, token)
					delete(r.pendingAuthentications, token)
				}
			}
			r.authMu.Unlock()
			return
		}

		message = strings.TrimSpace(message)
		parts := strings.Fields(message)
		if len(parts) == 0 {
			continue
		}
		command := parts[0]
		log.Printf("DEBUG: Control command from %s: %s, Args: %v", remoteAddr, command, parts[1:])

		switch command {
		case "REGISTER_HOST":
			if len(parts) != 1 {
				log.Printf("WARN: REGISTER_HOST received with unexpected arguments from %s: %v. Ignoring arguments.", remoteAddr, parts[1:])
			}
			// No need to lock here for generateMemorableID as it locks internally.
			newHostID := r.generateMemorableID()

			r.mu.Lock() // Lock for modifying hostControlConns and registeredHostID
			if oldHostID, alreadyRegistered := r.findHostByConn(conn); alreadyRegistered {
				log.Printf("WARN: Connection %s (previously '%s') is re-registering. Old ID will be removed.", remoteAddr, oldHostID)
				delete(r.hostControlConns, oldHostID)
			}
			r.hostControlConns[newHostID] = conn
			registeredHostID = newHostID
			r.mu.Unlock()

			log.Printf("INFO: Host registered from %s, assigned ID '%s'", remoteAddr, newHostID)
			fmt.Fprintf(conn, "HOST_REGISTERED %s\n", newHostID)

		case "INITIATE_CLIENT_SESSION":
			if len(parts) < 2 {
				fmt.Fprintln(conn, "ERROR Invalid INITIATE_CLIENT_SESSION. Usage: INITIATE_CLIENT_SESSION <target_host_id> [password]")
				continue
			}
			targetHostID := parts[1]
			passwordFromLauncher := ""
			if len(parts) > 2 {
				passwordFromLauncher = parts[2] // This could be an empty string if client sent " " then newline
			} else if len(parts) == 2 && strings.HasSuffix(message, " \n") {
				// Handle case where client sends "INITIATE_CLIENT_SESSION hostid \n" (empty password)
				// passwordFromLauncher remains ""
			}

			r.mu.Lock()
			hostControlConn, hostIsRegistered := r.hostControlConns[targetHostID]
			r.mu.Unlock()

			if !hostIsRegistered {
				log.Printf("WARN: Target host '%s' not found for client session from %s", targetHostID, remoteAddr)
				fmt.Fprintf(conn, "ERROR_HOST_NOT_FOUND %s\n", targetHostID)
				continue
			}

			requestToken := uuid.New().String()
			r.authMu.Lock()
			r.pendingAuthentications[requestToken] = PendingAuthRequest{
				launcherConn:  conn,
				targetHostID:  targetHostID,
				initiatedTime: time.Now(),
			}
			r.authMu.Unlock()

			log.Printf("INFO: Session for host '%s'. Sending VERIFY_PASSWORD_REQUEST (token %s) to host. Password provided by launcher: '%s'", targetHostID, requestToken, passwordFromLauncher)

			// Send the passwordFromLauncher (which could be empty if client sent none or " ")
			_, errSend := fmt.Fprintf(hostControlConn, "VERIFY_PASSWORD_REQUEST %s %s\n", requestToken, passwordFromLauncher)
			if errSend != nil {
				log.Printf("ERROR: Failed to send VERIFY_PASSWORD_REQUEST to host '%s': %v. Aborting auth.", targetHostID, errSend)
				fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Failed to contact host for auth\n")
				r.authMu.Lock()
				delete(r.pendingAuthentications, requestToken)
				r.authMu.Unlock()
				continue
			}

			go func(token string, launcherConnection net.Conn, targetHID string) {
				time.Sleep(authResponseTimeout)
				r.authMu.Lock()
				defer r.authMu.Unlock()
				if pendingReq, exists := r.pendingAuthentications[token]; exists {
					log.Printf("WARN: Timeout waiting for VERIFY_PASSWORD_RESPONSE from host '%s' for token %s.", targetHID, token)
					if pendingReq.launcherConn != nil {
						fmt.Fprintf(pendingReq.launcherConn, "ERROR_AUTHENTICATION_FAILED %s\n", targetHID)
					}
					delete(r.pendingAuthentications, token)
				}
			}(requestToken, conn, targetHostID)

		case "VERIFY_PASSWORD_RESPONSE":
			if len(parts) < 3 {
				log.Printf("WARN: Invalid VERIFY_PASSWORD_RESPONSE from %s: %s", remoteAddr, message)
				continue
			}
			requestToken := parts[1]
			isValidStr := strings.ToLower(parts[2])

			r.authMu.Lock()
			pendingReq, ok := r.pendingAuthentications[requestToken]
			if !ok {
				log.Printf("WARN: Received VERIFY_PASSWORD_RESPONSE for unknown/expired token %s from %s", requestToken, remoteAddr)
				r.authMu.Unlock()
				continue
			}

			if registeredHostID != pendingReq.targetHostID {
				log.Printf("WARN: VERIFY_PASSWORD_RESPONSE token %s valid, but received from unexpected host %s (expected %s). Ignoring.",
					requestToken, registeredHostID, pendingReq.targetHostID)
				r.authMu.Unlock()
				continue
			}

			delete(r.pendingAuthentications, requestToken)
			r.authMu.Unlock()

			if pendingReq.launcherConn == nil {
				log.Printf("CRITICAL: Launcher connection for token %s is nil. Cannot proceed.", requestToken)
				continue
			}

			if isValidStr == "true" {
				log.Printf("INFO: Password VERIFIED for host '%s' (token %s) by launcher %s. Proceeding with session setup.",
					pendingReq.targetHostID, requestToken, pendingReq.launcherConn.RemoteAddr())

				r.mu.Lock()
				hostCtlConn, hostStillRegistered := r.hostControlConns[pendingReq.targetHostID]
				r.mu.Unlock()

				if !hostStillRegistered {
					log.Printf("WARN: Host '%s' disconnected after password verification for token %s. Aborting session.", pendingReq.targetHostID, requestToken)
					fmt.Fprintf(pendingReq.launcherConn, "ERROR_HOST_NOT_FOUND %s\n", pendingReq.targetHostID)
					continue
				}
				r.setupSession(pendingReq.launcherConn, pendingReq.targetHostID, hostCtlConn)
			} else {
				log.Printf("WARN: Password verification FAILED for host '%s' (token %s) by launcher %s.",
					pendingReq.targetHostID, requestToken, pendingReq.launcherConn.RemoteAddr())
				fmt.Fprintf(pendingReq.launcherConn, "ERROR_AUTHENTICATION_FAILED %s\n", pendingReq.targetHostID)
			}

		default:
			log.Printf("WARN: Unknown control command from %s: '%s'", remoteAddr, message)
			fmt.Fprintf(conn, "ERROR Unknown command: %s\n", command)
		}
	}
}

// findHostByConn iterates through hostControlConns to find if a connection is already registered.
// This is useful if a host tries to re-register with the same connection.
func (r *RelayServer) findHostByConn(conn net.Conn) (hostID string, found bool) {
	for id, c := range r.hostControlConns {
		if c == conn {
			return id, true
		}
	}
	return "", false
}

// setupSession proceeds to establish the data relay after successful checks.
func (r *RelayServer) setupSession(launcherConn net.Conn, targetHostID string, hostControlConn net.Conn) {
	sessionToken := uuid.New().String()
	dataListener, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Printf("ERROR: Failed to create dynamic data listener for session %s: %v", sessionToken, err)
		fmt.Fprintf(launcherConn, "ERROR_RELAY_INTERNAL Failed to create data port\n")
		return
	}

	tcpAddr, ok := dataListener.Addr().(*net.TCPAddr)
	if !ok {
		log.Printf("ERROR: Session %s: Could not get TCP address from data listener.", sessionToken)
		fmt.Fprintf(launcherConn, "ERROR_RELAY_INTERNAL Failed to get data port details\n")
		dataListener.Close()
		return
	}
	dynamicPort := tcpAddr.Port
	log.Printf("INFO: Session %s for host '%s': Dynamic data listener on port %d", sessionToken, targetHostID, dynamicPort)

	fmt.Fprintf(launcherConn, "SESSION_READY %d %s\n", dynamicPort, sessionToken)
	log.Printf("INFO: Session %s: Notified launcher %s to have client.exe connect to relay's public IP on port %d",
		sessionToken, launcherConn.RemoteAddr(), dynamicPort)

	if hostControlConn != nil {
		_, errSend := fmt.Fprintf(hostControlConn, "CREATE_TUNNEL %d %s\n", dynamicPort, sessionToken)
		if errSend != nil {
			log.Printf("ERROR: Session %s: Failed to send CREATE_TUNNEL (port %d) to host '%s' (%s): %v. Aborting session.",
				sessionToken, dynamicPort, targetHostID, hostControlConn.RemoteAddr(), errSend)
			fmt.Fprintf(launcherConn, "ERROR_RELAY_INTERNAL Failed to notify host.\n")
			dataListener.Close()
			return
		}
		log.Printf("INFO: Session %s: Notified host '%s' (control %s) to create tunnel to relay's public IP on port %d",
			sessionToken, targetHostID, hostControlConn.RemoteAddr(), dynamicPort)
	} else {
		log.Printf("CRITICAL: Session %s: Host '%s' registered but control connection is nil for setupSession. Aborting.", sessionToken, targetHostID)
		fmt.Fprintf(launcherConn, "ERROR_RELAY_INTERNAL Host control connection issue.\n")
		dataListener.Close()
		return
	}
	go r.manageDataSession(dataListener, sessionToken, targetHostID, dynamicPort)
}

// manageDataSession waits for two connections on the dataListener.
func (r *RelayServer) manageDataSession(dataListener net.Listener, sessionToken string, hostID string, port int) {
	defer dataListener.Close()
	log.Printf("INFO: Session %s (Host '%s'): Waiting for data connections on port %d (timeout: %s for each, plus ident)", sessionToken, hostID, port, dataConnTimeout)

	var clientAppConn, hostProxyConn net.Conn
	var wg sync.WaitGroup
	wg.Add(2)

	connChan := make(chan net.Conn, 2)
	errChan := make(chan error, 2)

	go func() {
		defer wg.Done()
		conn, err := dataListener.Accept()
		if err != nil {
			if !strings.Contains(err.Error(), "use of closed network connection") {
				errChan <- fmt.Errorf("accept1 failed for session %s: %w", sessionToken, err)
			}
			return
		}
		connChan <- conn
	}()

	go func() {
		defer wg.Done()
		conn, err := dataListener.Accept()
		if err != nil {
			if !strings.Contains(err.Error(), "use of closed network connection") {
				errChan <- fmt.Errorf("accept2 failed for session %s: %w", sessionToken, err)
			}
			return
		}
		connChan <- conn
	}()

	acceptTimeout := time.After(dataConnTimeout*2 + identTimeout + 2*time.Second)
	var acceptedConns []net.Conn

LoopAccept:
	for i := 0; i < 2; i++ {
		select {
		case conn := <-connChan:
			log.Printf("INFO: Session %s: Accepted data connection from %s on port %d", sessionToken, conn.RemoteAddr(), port)
			acceptedConns = append(acceptedConns, conn)
		case err := <-errChan:
			log.Printf("ERROR: Session %s: Error accepting data connection: %v", sessionToken, err)
		case <-acceptTimeout:
			log.Printf("WARN: Session %s: Timed out waiting for all data connections on port %d. Received %d/2.", sessionToken, port, len(acceptedConns))
			break LoopAccept
		}
	}
	wg.Wait()

	if len(acceptedConns) < 2 {
		log.Printf("WARN: Session %s: Did not receive two data connections for port %d. Closing %d accepted connections.", sessionToken, port, len(acceptedConns))
		for _, conn := range acceptedConns {
			conn.Close()
		}
		return
	}

	for _, conn := range acceptedConns {
		conn.SetReadDeadline(time.Now().Add(identTimeout))
		identifier, err := bufio.NewReader(conn).ReadString('\n')
		conn.SetReadDeadline(time.Time{})

		if err != nil {
			log.Printf("WARN: Session %s: Failed to read identification from %s on port %d: %v. Closing it.", sessionToken, conn.RemoteAddr(), port, err)
			conn.Close()
			continue
		}
		identifier = strings.TrimSpace(identifier)
		parts := strings.Fields(identifier)

		if len(parts) != 3 || parts[0] != "SESSION_TOKEN" || parts[1] != sessionToken {
			log.Printf("WARN: Session %s: Invalid identification from %s on port %d: '%s'. Closing it.", sessionToken, conn.RemoteAddr(), port, identifier)
			conn.Close()
			continue
		}
		sourceType := parts[2]
		log.Printf("INFO: Session %s: Connection %s on port %d identified as %s", sessionToken, conn.RemoteAddr(), port, sourceType)

		if sourceType == "CLIENT_APP" {
			if clientAppConn != nil {
				log.Printf("WARN: Session %s: Duplicate CLIENT_APP connection from %s. Closing new one.", sessionToken, conn.RemoteAddr())
				conn.Close()
			} else {
				clientAppConn = conn
			}
		} else if sourceType == "HOST_PROXY" {
			if hostProxyConn != nil {
				log.Printf("WARN: Session %s: Duplicate HOST_PROXY connection from %s. Closing new one.", sessionToken, conn.RemoteAddr())
				conn.Close()
			} else {
				hostProxyConn = conn
			}
		} else {
			log.Printf("WARN: Session %s: Unknown source type '%s' from %s. Closing it.", sessionToken, sourceType, conn.RemoteAddr())
			conn.Close()
		}
	}

	if clientAppConn == nil || hostProxyConn == nil {
		log.Printf("WARN: Session %s: Failed to establish complete data relay on port %d. Client identified: %t, Host proxy identified: %t. Aborting.",
			sessionToken, port, clientAppConn != nil, hostProxyConn != nil)
		if clientAppConn != nil {
			clientAppConn.Close()
		}
		if hostProxyConn != nil {
			hostProxyConn.Close()
		}
		return
	}

	log.Printf("INFO: Session %s: Data connections established on port %d. Relaying between CLIENT_APP (%s) and HOST_PROXY (%s).",
		sessionToken, port, clientAppConn.RemoteAddr(), hostProxyConn.RemoteAddr())

	var relayWg sync.WaitGroup
	relayWg.Add(2)

	go func() {
		defer relayWg.Done()
		defer clientAppConn.Close()
		defer hostProxyConn.Close()
		written, err := io.Copy(hostProxyConn, clientAppConn)
		if err != nil && !isNetworkCloseError(err) {
			log.Printf("ERROR: Session %s: Copying CLIENT_APP to HOST_PROXY: %v (bytes: %d)", sessionToken, err, written)
		} else {
			log.Printf("INFO: Session %s: CLIENT_APP to HOST_PROXY stream ended. Bytes: %d. Error (if any): %v", sessionToken, written, err)
		}
	}()
	go func() {
		defer relayWg.Done()
		defer hostProxyConn.Close()
		defer clientAppConn.Close()
		written, err := io.Copy(clientAppConn, hostProxyConn)
		if err != nil && !isNetworkCloseError(err) {
			log.Printf("ERROR: Session %s: Copying HOST_PROXY to CLIENT_APP: %v (bytes: %d)", sessionToken, err, written)
		} else {
			log.Printf("INFO: Session %s: HOST_PROXY to CLIENT_APP stream ended. Bytes: %d. Error (if any): %v", sessionToken, written, err)
		}
	}()
	relayWg.Wait()
	log.Printf("INFO: Session %s: Relaying ended for port %d.", sessionToken, port)
}

// isNetworkCloseError checks if the error is a common network connection closed error.
func isNetworkCloseError(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "forcibly closed by the remote host") // Windows
}
