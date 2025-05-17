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

	"github.com/google/uuid" // For session tokens
)

const (
	controlPort     = "34000"          // Main port for control messages from hosts and launchers
	dataConnTimeout = 15 * time.Second // Timeout for client/host proxy to connect to data port
	identTimeout    = 5 * time.Second  // Timeout for an incoming data connection to identify itself
)

// Word lists for generating memorable IDs
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

// RelayServer manages the state of the relay.
type RelayServer struct {
	hostControlConns map[string]net.Conn // hostID -> control connection from host's sidecar
	mu               sync.Mutex
}

// NewRelayServer creates a new relay server instance.
func NewRelayServer() *RelayServer {
	rand.Seed(time.Now().UnixNano()) // Seed random number generator
	return &RelayServer{
		hostControlConns: make(map[string]net.Conn),
	}
}

// generateMemorableID creates a unique, memorable ID for a host.
// It must be called with r.mu locked.
func (r *RelayServer) generateMemorableID() string {
	maxAttempts := 10 // Max attempts to generate a unique AdjectiveNoun ID
	for attempt := 0; attempt < maxAttempts; attempt++ {
		adj := adjectives[rand.Intn(len(adjectives))]
		noun := nouns[rand.Intn(len(nouns))]
		id := adj + noun
		if _, exists := r.hostControlConns[id]; !exists {
			return id
		}
	}
	// If still not unique after maxAttempts, append a number
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
	log.SetFlags(log.LstdFlags | log.Lshortfile) // Include file and line number in logs
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

// handleControlConnection processes messages from a control connection (either host sidecar or launcher).
func (r *RelayServer) handleControlConnection(conn net.Conn) {
	defer conn.Close()
	remoteAddr := conn.RemoteAddr().String()
	log.Printf("INFO: New control connection from: %s", remoteAddr)
	reader := bufio.NewReader(conn)

	var registeredHostID string // Store the ID this connection registered, for cleanup

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
				// Only remove if this is still the active connection for this hostID
				if currentConn, ok := r.hostControlConns[registeredHostID]; ok && currentConn == conn {
					log.Printf("INFO: Host '%s' (control conn %s) disconnected. Removing from registry.", registeredHostID, remoteAddr)
					delete(r.hostControlConns, registeredHostID)
				} else if ok {
					log.Printf("INFO: Host '%s' disconnected (control conn %s), but a newer connection (%s) exists. Not removing from registry.", registeredHostID, remoteAddr, currentConn.RemoteAddr())
				} else {
					log.Printf("INFO: Host '%s' (control conn %s) disconnected. Was already removed from registry.", registeredHostID, remoteAddr)
				}
				r.mu.Unlock()
			}
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
			// Host no longer sends an ID. Server generates it.
			r.mu.Lock()
			// If this connection previously registered an ID, and is trying to re-register,
			// treat it as a new registration. Clean up old ID if necessary.
			if registeredHostID != "" {
				if r.hostControlConns[registeredHostID] == conn { // if it's the same connection re-registering
					log.Printf("WARN: Host connection %s (previously '%s') is re-registering. Old ID will be orphaned if not re-used by a new connection.", remoteAddr, registeredHostID)
					// We don't delete the old hostID here immediately, because another connection might take it.
					// The new ID generation will ensure uniqueness.
				}
			}

			newHostID := r.generateMemorableID() // Must be called while lock is held
			r.hostControlConns[newHostID] = conn
			registeredHostID = newHostID // Update the ID associated with this specific connection
			r.mu.Unlock()

			log.Printf("INFO: Host registered from %s, assigned ID '%s'", remoteAddr, newHostID)
			fmt.Fprintf(conn, "HOST_REGISTERED %s\n", newHostID)

		case "INITIATE_CLIENT_SESSION":
			if len(parts) < 2 {
				fmt.Fprintln(conn, "ERROR Invalid INITIATE_CLIENT_SESSION. Usage: INITIATE_CLIENT_SESSION <target_host_id>")
				continue
			}
			targetHostID := parts[1]

			r.mu.Lock()
			hostControlConn, hostIsRegistered := r.hostControlConns[targetHostID]
			r.mu.Unlock()

			if !hostIsRegistered {
				log.Printf("WARN: Target host '%s' not found for client session from %s", targetHostID, remoteAddr)
				fmt.Fprintf(conn, "ERROR_HOST_NOT_FOUND %s\n", targetHostID)
				continue
			}

			sessionToken := uuid.New().String()
			dataListener, err := net.Listen("tcp", ":0") // OS chooses a free port
			if err != nil {
				log.Printf("ERROR: Failed to create dynamic data listener for session %s: %v", sessionToken, err)
				fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Failed to create data port\n")
				continue
			}

			tcpAddr, ok := dataListener.Addr().(*net.TCPAddr)
			if !ok {
				log.Printf("ERROR: Session %s: Could not get TCP address from data listener.", sessionToken)
				fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Failed to get data port details\n")
				dataListener.Close()
				continue
			}
			dynamicPort := tcpAddr.Port
			log.Printf("INFO: Session %s for host '%s': Dynamic data listener on port %d", sessionToken, targetHostID, dynamicPort)

			fmt.Fprintf(conn, "SESSION_READY %d %s\n", dynamicPort, sessionToken)
			log.Printf("INFO: Session %s: Notified launcher %s to have client.exe connect to relay's public IP on port %d", sessionToken, remoteAddr, dynamicPort)

			if hostControlConn != nil {
				_, errSend := fmt.Fprintf(hostControlConn, "CREATE_TUNNEL %d %s\n", dynamicPort, sessionToken)
				if errSend != nil {
					log.Printf("ERROR: Session %s: Failed to send CREATE_TUNNEL (port %d) to host '%s' (%s): %v. Aborting session.", sessionToken, dynamicPort, targetHostID, hostControlConn.RemoteAddr(), errSend)
					fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Failed to notify host.\n")
					dataListener.Close()
					// Consider if we should remove the host if its control conn fails here
					// For now, we don't, as it might recover or re-register.
					// The session initiator (launcher) will get an error.
					continue
				}
				log.Printf("INFO: Session %s: Notified host '%s' (control %s) to create tunnel to relay's public IP on port %d", sessionToken, targetHostID, hostControlConn.RemoteAddr(), dynamicPort)
			} else {
				// This case should ideally not happen if hostIsRegistered is true and lock was handled correctly.
				log.Printf("CRITICAL: Session %s: Host '%s' registered but control connection is nil. Aborting.", sessionToken, targetHostID)
				fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Host control connection issue.\n")
				dataListener.Close()
				continue
			}

			go r.manageDataSession(dataListener, sessionToken, targetHostID, dynamicPort)
		default:
			log.Printf("WARN: Unknown control command from %s: '%s'", remoteAddr, message)
			fmt.Fprintf(conn, "ERROR Unknown command: %s\n", command)
		}
	}
}

// manageDataSession waits for two connections on the dataListener.
func (r *RelayServer) manageDataSession(dataListener net.Listener, sessionToken string, hostID string, port int) {
	defer dataListener.Close()
	log.Printf("INFO: Session %s (Host '%s'): Waiting for data connections on port %d (timeout: %s)", sessionToken, hostID, port, dataConnTimeout*2)

	var clientAppConn, hostProxyConn net.Conn
	var wg sync.WaitGroup
	wg.Add(2)

	connChan := make(chan net.Conn, 2)
	errChan := make(chan error, 2) // To catch errors from Accept

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

	acceptTimeout := time.After(dataConnTimeout*2 + identTimeout) // Slightly longer to accommodate ident
	var acceptedConns []net.Conn

LoopAccept:
	for i := 0; i < 2; i++ {
		select {
		case conn := <-connChan:
			log.Printf("INFO: Session %s: Accepted data connection from %s on port %d", sessionToken, conn.RemoteAddr(), port)
			acceptedConns = append(acceptedConns, conn)
		case err := <-errChan:
			log.Printf("ERROR: Session %s: Error accepting data connection: %v", sessionToken, err)
			// Don't break loop immediately, one might have succeeded.
			// The logic below will handle if not enough conns.
		case <-acceptTimeout:
			if len(acceptedConns) < 2 {
				log.Printf("WARN: Session %s: Timed out waiting for all data connections on port %d. Received %d/2.", sessionToken, port, len(acceptedConns))
			}
			break LoopAccept
		}
	}
	wg.Wait() // Wait for Accept goroutines to finish

	// Close any connections if we didn't get two
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
		conn.SetReadDeadline(time.Time{}) // Clear deadline

		if err != nil {
			log.Printf("WARN: Session %s: Failed to read identification from %s on port %d: %v. Closing it.", sessionToken, conn.RemoteAddr(), port, err)
			conn.Close()
			continue // Try to identify the other connection if this one failed
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

	// After attempting to identify both, check if we have both client and host proxy
	if clientAppConn == nil || hostProxyConn == nil {
		log.Printf("WARN: Session %s: Failed to establish complete data relay on port %d. Client connected: %t, Host proxy connected: %t. Aborting.",
			sessionToken, port, clientAppConn != nil, hostProxyConn != nil)
		if clientAppConn != nil && clientAppConn != hostProxyConn { // Ensure not closing same conn twice if one was nil
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
		defer hostProxyConn.Close() // Ensure both are closed when one side finishes/errors
		written, err := io.Copy(hostProxyConn, clientAppConn)
		if err != nil && !isNetworkCloseError(err) {
			log.Printf("ERROR: Session %s: Copying CLIENT_APP to HOST_PROXY: %v (bytes: %d)", sessionToken, err, written)
		} else {
			log.Printf("INFO: Session %s: CLIENT_APP to HOST_PROXY stream ended. Bytes: %d. Error: %v", sessionToken, written, err)
		}
	}()
	go func() {
		defer relayWg.Done()
		defer hostProxyConn.Close()
		defer clientAppConn.Close() // Ensure both are closed
		written, err := io.Copy(clientAppConn, hostProxyConn)
		if err != nil && !isNetworkCloseError(err) {
			log.Printf("ERROR: Session %s: Copying HOST_PROXY to CLIENT_APP: %v (bytes: %d)", sessionToken, err, written)
		} else {
			log.Printf("INFO: Session %s: HOST_PROXY to CLIENT_APP stream ended. Bytes: %d. Error: %v", sessionToken, written, err)
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
		strings.Contains(s, "broken pipe")
}
