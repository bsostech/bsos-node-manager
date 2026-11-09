// cactl manages the embedded CA material.
// By default the server self-signs a root on first boot and stores the root private key encrypted
// (enough for development or a single machine). For production, keep the root private key out of the
// system entirely: use one of the two paths below, then start the server:
//
//	cactl init-offline [--root-key-out root-ca.key]
//	    Generates a brand-new two-tier CA on the spot and writes the root private key to a file
//	    (take it off the host and keep it safe); the DB keeps only the root cert + intermediate cert/key.
//
//	cactl import --root-cert root.pem --inter-cert inter.pem --inter-key inter.key
//	    Brings in an existing enterprise PKI: the platform acts as an intermediate under it and the
//	    root stays entirely in your hands.
//
//	cactl show
//	    Shows the current CA status (root online/offline, expiry and days left per tier, root fingerprint).
//
//	cactl rotate-intermediate
//	    Routine renewal: re-signs the intermediate with the same private key; root and agents are
//	    unaffected. With the root in the DB it signs directly; with the root offline it takes two steps:
//	      cactl rotate-intermediate --csr-out inter.csr        generate a CSR for the root holder to sign
//	      cactl rotate-intermediate --inter-cert inter.pem     import the signed certificate
//	    --new-key is for compromise response: it replaces the private key and every node must re-join
//	    (offline mode also needs --key-out / --inter-key).
//	    Restart the server afterwards (the CA is loaded only at startup).
//
// Requires CA_MASTER_KEY (64 hex, or CA_MASTER_KEY_FILE pointing to a file) and DATABASE_URL; writes
// ca_certificates directly. init-offline / import must run before the server has bootstrapped (no CA
// material in the DB); they refuse to overwrite existing material.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"besu-manager/backend/internal/ca"
	"besu-manager/backend/internal/store"
)

func main() {
	_ = godotenv.Load()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cactl <init-offline|import|show|rotate-intermediate> [flags]")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dbURL := getenv("DATABASE_URL", "postgres://besu:besu@localhost:5432/besu_manager")
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	st := store.New(pool)
	// A first deployment builds the CA before the server's first start (up.sh --offline-root), when nobody
	// has created the tables yet; schema.sql is idempotent and is the same file the server runs at startup.
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("create tables: %v", err)
	}

	switch os.Args[1] {
	case "show":
		cmdShow(ctx, st)
	case "init-offline":
		cmdInitOffline(ctx, st, os.Args[2:])
	case "import":
		cmdImport(ctx, st, os.Args[2:])
	case "rotate-intermediate":
		cmdRotateIntermediate(ctx, st, os.Args[2:])
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func cmdShow(ctx context.Context, st *store.Store) {
	mat, err := st.GetCAMaterial(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if mat == nil {
		// deploy/platform/docker-compose/up.sh matches "no CA material yet" to decide whether to create the CA; change both together.
		fmt.Println("no CA material yet (the server self-signs a bootstrap CA on first start; run init-offline or import before that)")
		return
	}
	root := mustParse(mat.RootCertPEM)
	inter := mustParse(mat.IntCertPEM)
	sum := sha256.Sum256(root.Raw)
	rootMode := "offline (no root private key in the DB) ✅"
	if mat.RootOnline() {
		rootMode = "online (root private key stored encrypted in the DB) ⚠️"
	}
	fmt.Printf("root         %s\n", root.Subject.CommonName)
	fmt.Printf("  status     %s\n", rootMode)
	fmt.Printf("  expires    %s\n", root.NotAfter.Format(time.RFC3339))
	fmt.Printf("  digest     sha256:%s\n", hex.EncodeToString(sum[:]))
	fmt.Printf("intermediate %s\n", inter.Subject.CommonName)
	left := time.Until(inter.NotAfter)
	fmt.Printf("  expires    %s (%d days left)\n", inter.NotAfter.Format(time.RFC3339), int(left.Hours()/24))
	if mat.RootOnline() {
		fmt.Println("\n⚠️  The root private key is in the DB. This is only for development and trials; for production use init-offline or import so the root stays out of the system.")
	}
	if left < ca.RenewBefore {
		fmt.Println("⚠️  The intermediate expires soon. Once it expires, every agent must re-join. Run cactl rotate-intermediate.")
	}
}

func cmdRotateIntermediate(ctx context.Context, st *store.Store, args []string) {
	fs := flag.NewFlagSet("rotate-intermediate", flag.ExitOnError)
	newKey := fs.Bool("new-key", false, "compromise response: replace the private key. Certificates signed by the old key stop working immediately and every agent must re-join")
	yes := fs.Bool("yes", false, "with --new-key: skip the confirmation prompt")
	csrOut := fs.String("csr-out", "", "offline root: CSR output file (for the root holder to sign)")
	keyOut := fs.String("key-out", "inter-new.key", "with --new-key --csr-out: output file for the new private key")
	interCert := fs.String("inter-cert", "", "offline root: intermediate certificate PEM signed by the root holder")
	interKey := fs.String("inter-key", "", "with --new-key --inter-cert: the new private key file written by the --csr-out step")
	_ = fs.Parse(args)
	if *csrOut != "" && *interCert != "" {
		log.Fatal("--csr-out and --inter-cert are separate steps; pass only one at a time")
	}
	if (*interKey != "") != (*newKey && *interCert != "") {
		log.Fatal("--inter-key is used only with --new-key --inter-cert, and is required there")
	}

	master := mustMaster()
	org := mustOrgID()
	mat, err := st.GetCAMaterial(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if mat == nil {
		log.Fatal("no CA material in the DB, no intermediate to rotate")
	}
	if *newKey && *interCert == "" && !*yes {
		confirmNewKey()
	}

	var nm *ca.Material
	switch {
	case *csrOut != "":
		if _, err := os.Stat(*csrOut); err == nil {
			log.Fatalf("%s already exists, aborting to avoid overwriting it", *csrOut)
		}
		keyPEM, csrPEM, err := ca.IntermediateCSR(master, mat, org, *newKey)
		if err != nil {
			log.Fatal(err)
		}
		if keyPEM != nil {
			if _, err := os.Stat(*keyOut); err == nil {
				log.Fatalf("%s already exists, aborting to avoid overwriting it", *keyOut)
			}
			if err := os.WriteFile(*keyOut, keyPEM, 0o600); err != nil {
				log.Fatal(err)
			}
		}
		if err := os.WriteFile(*csrOut, csrPEM, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("CSR written to %s. Have the root holder sign it as a CA (basicConstraints CA:TRUE, pathlen:0, keyCertSign),\n", *csrOut)
		if keyPEM != nil {
			fmt.Printf("then run: cactl rotate-intermediate --new-key --inter-cert <signed-cert> --inter-key %s\n", *keyOut)
		} else {
			fmt.Println("then run: cactl rotate-intermediate --inter-cert <signed-cert>")
		}
		return
	case *interCert != "":
		var keyPEM []byte
		if *newKey {
			keyPEM = readFile(*interKey)
		}
		nm, err = ca.ReplaceIntermediate(master, mat, readFile(*interCert), keyPEM, time.Now().UTC())
		if err != nil {
			log.Fatalf("verify imported certificate: %v", err)
		}
	case !mat.RootOnline():
		log.Fatal("the root is not in the system, so the platform cannot sign. Use --csr-out to generate a CSR for the root holder to sign, then import it with --inter-cert")
	case *newKey:
		nm, err = ca.RotateIntermediate(master, mat, time.Now().UTC())
	default:
		nm, err = ca.RenewIntermediate(master, mat, time.Now().UTC())
	}
	if err != nil {
		log.Fatal(err)
	}

	c, err := ca.FromMaterial(master, org, nm)
	if err != nil {
		log.Fatalf("load new material: %v", err)
	}
	if err := st.UpdateIntermediate(ctx, nm.IntCertPEM, nm.IntKeyEnc, c.Inter.NotAfter); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("✅ intermediate replaced, expires %s. Root digest unchanged: sha256:%s\n", c.Inter.NotAfter.Format(time.RFC3339), c.RootDigest())
	if *newKey {
		fmt.Println("   Restart the server. Certificates signed by the old key no longer work: every node must re-join with a new join code.")
		if *interKey != "" {
			fmt.Printf("   %s is now stored encrypted in the DB; delete this file.\n", *interKey)
		}
	} else {
		fmt.Println("   Restart the server. The private key is unchanged, so agents are unaffected; they get the new intermediate at their next leaf certificate renewal.")
	}
}

// confirmNewKey asks a human to confirm, since replacing the private key forces every agent to re-join.
func confirmNewKey() {
	fmt.Println("⚠️  --new-key is for compromise response: after the intermediate private key is replaced, certificates signed by the old key stop working immediately")
	fmt.Println("   and every node must re-join. Do not use this option for routine renewal.")
	fmt.Print("Replace the private key? Type yes to continue: ")
	var ans string
	_, _ = fmt.Scanln(&ans)
	if ans != "yes" {
		log.Fatal("cancelled")
	}
}

func cmdInitOffline(ctx context.Context, st *store.Store, args []string) {
	fs := flag.NewFlagSet("init-offline", flag.ExitOnError)
	out := fs.String("root-key-out", "root-ca.key", "root private key output file (move it off this host and keep it safe)")
	_ = fs.Parse(args)

	master := mustMaster()
	org := mustOrgID()
	mustEmpty(ctx, st)

	if _, err := os.Stat(*out); err == nil {
		log.Fatalf("%s already exists, aborting to avoid overwriting an existing root private key", *out)
	}
	mat, rootKeyPEM, err := ca.GenerateOffline(master, org, time.Now().UTC())
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, rootKeyPEM, 0o600); err != nil {
		log.Fatal(err)
	}
	saveAndReport(ctx, st, master, org, mat)
	fmt.Printf("\n⚠️  Root private key written to %s. It is the source of all trust in this system.\n", *out)
	fmt.Println("   Move it to offline storage or a vault now and delete it from this host; day-to-day issuance uses only the intermediate and does not need it.")
}

func cmdImport(ctx context.Context, st *store.Store, args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	rootCert := fs.String("root-cert", "", "root certificate PEM file (certificate only, no private key)")
	interCert := fs.String("inter-cert", "", "intermediate certificate PEM file")
	interKey := fs.String("inter-key", "", "intermediate private key PEM file (ECDSA P-256, SEC1 or PKCS#8)")
	_ = fs.Parse(args)
	if *rootCert == "" || *interCert == "" || *interKey == "" {
		log.Fatal("--root-cert / --inter-cert / --inter-key are all required")
	}

	master := mustMaster()
	org := mustOrgID()
	mustEmpty(ctx, st)

	mat, err := ca.ImportMaterial(master, readFile(*rootCert), readFile(*interCert), readFile(*interKey))
	if err != nil {
		log.Fatalf("verify imported material: %v", err)
	}
	saveAndReport(ctx, st, master, org, mat)
	fmt.Println("\n✅ External root imported: the platform runs as an intermediate and the root private key is not in the system.")
}

// saveAndReport derives the expiry dates via FromMaterial (which also confirms the intermediate private key
// decrypts) and stores the material in the DB.
func saveAndReport(ctx context.Context, st *store.Store, master []byte, org string, mat *ca.Material) {
	c, err := ca.FromMaterial(master, org, mat)
	if err != nil {
		log.Fatalf("load material (master key or private key mismatch?): %v", err)
	}
	if err := st.SaveCAMaterial(ctx, mat, c.Root.NotAfter, c.Inter.NotAfter); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("org          : %s\n", org)
	fmt.Printf("root digest  : sha256:%s\n", c.RootDigest())
	fmt.Println("(this digest is embedded in join tokens automatically; agents pin it to verify the platform against man-in-the-middle attacks)")
}

func mustEmpty(ctx context.Context, st *store.Store) {
	mat, err := st.GetCAMaterial(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if mat != nil {
		log.Fatal("The DB already has CA material; aborting to avoid breaking existing trust. " +
			"To start over, clear ca_certificates (and the dependent issued/revoked tables)")
	}
}

func mustMaster() []byte {
	master, err := ca.MasterKeyFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	return master
}

func mustParse(p []byte) *x509.Certificate {
	block, _ := pem.Decode(p)
	if block == nil {
		log.Fatal("cannot parse certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		log.Fatal(err)
	}
	return cert
}

func readFile(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read %s: %v", path, err)
	}
	return b
}

// mustOrgID runs the same check as the server, so cactl does not build a CA with an invalid id that the
// server then rejects at startup.
func mustOrgID() string {
	id := os.Getenv("ORG_ID")
	if err := ca.CheckOrgID(id); err != nil {
		log.Fatal(err)
	}
	return id
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
