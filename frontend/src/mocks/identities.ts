/**
 * Identity of every mock node: full 128-hex public key, the address derived from it (keccak256(pubkey)[12:], precomputed
 * with the backend's enode.Address and hard-coded, since the frontend has no keccak and adding a dependency just for mocks is not worth it), and IP.
 * The list, detail, Peers, shared list and validators all read from here, so a node has the same key and address on every page.
 */
export const IDENTITIES = {
  'node-a1': { pub: '7578b4c11fdfd65049f14226d44e283e8f16e6390538c590c22acd7f3b60d42adce22d17e9bbb0fa487495bd0e9b1ff63fe23f345e2c2ae0b8b4d12438d967bf', addr: '0xde6b63740f9fd8fb42c994efae86a1880dd89a7e', ip: '10.0.0.1' },
  'node-a2': { pub: '9e380ad3072f11cf9a1f540e3e3997316ba245fff33f92b36185e01a0d5e3359793622533b471ee17ae5e6327e93efe0995d769b4dc5b8cfd844e24084055796', addr: '0x1c076da2b25bcd9d2276603a9bfdcf440a41e6e2', ip: '10.0.0.2' },
  'node-a3': { pub: '98ed39aa5ac377a7faaf5532b7bc8739a9d9b80fbdaaaf52af43477a5933ec600b21aff66d1e2c4b2d898ceffdd508ec60c82cdb443909e13e0cd92211a22d27', addr: '0x6283cf4f5fee769d4565b890ea37b2a42c250951', ip: '10.0.0.3' },
  'node-a4': { pub: '3c85bd255d240a9cd897ef844e706d18fb249e6b6f21298c2d8f535d5f01e469a2f9e3f442c2c46b687ada2bd8be16b37bd5aa3647aaebe5d7bb9acb6de61c65', addr: '0x85a0dcadd7709b3ae129d2df8f7b875b1e7945d9', ip: '10.0.0.4' },
  'node-a5': { pub: '496d9510e429ee9826240eab0351b217e7e3c245c3070058a1d314bc54ecc67f7f9b67c56915b8883d2079e1e08d7a6c038aa9e99360b806867f568288c9c6e5', addr: '0xea201be832cd7469c13532314c3a7420a240d163', ip: '10.0.0.5' },
  'node-a6': { pub: 'dcd3719ac7d41c982673695088bfc50f5eaea3f94a3287513542264ba5eb60d6410eb9730a43aea71cfb208dd7bbcb86d615355c41bf3ff64542be1e1a965b96', addr: '0xbd721d9ec6d575daf1be0519a4e72cd36b7505b3', ip: '10.0.0.6' },
  'node-a7': { pub: '6930b1ef2d4cecd4d23a84becc3b9b990b57994915c69e7b8eea9f27a5181ccbfc101d30164f5a209bc32a73f8209f3188df65d6f207f63e80a5f0758bbd1ff7', addr: '0x0302f56a54548acd2425a48e37bc885499702af9', ip: '10.0.0.7' },
  'node-a8': { pub: 'e3476459d59a0a6e77789617fbc82b3ac03c1a2a5108e91fdd89ee809cf6bb83033809a87d41d7e242f532576ff910d4c2a351a13b087b9a439a0fa371708da6', addr: '0x516918b96bd23f51bcb4f1228ee7c46ceb1a78f0', ip: '10.0.0.8' },
  'node-a9': { pub: 'beb33abbe65f3a34ee698cc3e836daaf95c738058abe8eb1c7190fa1e888e2916d110e5cfcd3f25a01877cdb91a15d8531d73f349c0c257f969e482911bc16de', addr: '0x9fd1d1e5a7a7857f1222672ed2d34df2ba031285', ip: '10.0.0.9' },
  'node-a10': { pub: 'b31a171daa538f92aba7e081071db1b3733f615b639982355b21121669dc5e7a56d91e97e6cc4a06915c10005e09c1e1c7b1b955f11c117dfbd9d9233bca5e35', addr: '0xb7fb8dd8bdf41ecb39a9c131060d00fdfa717934', ip: '10.0.0.10' },
  'node-a11': { pub: '4ff2584ef0ae4120c8679c1f914f5cc9086a23b85d7c89fcdc84e8c4c724c7e7bd83925dbdf7252b5cd8705fe841e873c99e045073aa5b25e2182e0fda062d95', addr: '0x906f67329af94450d0ba5ad2fd309c570adcbf95', ip: '10.0.0.11' },
  // Other orgs: an org-b validator (connected to several of our nodes), an unknown validator (not connected, no recent blocks), one org-c non-validator
  'org-b-v1': { pub: 'a1649aff9e3faa22900ea2bcd09f77917b48cb2b0c947ed21e4a284f8f3364a7ec5286598856908749d807c39f083a9c15769ab4680c38cd0b6566061343109e', addr: '0x274f1dbff1ab1fef430082d85afb2a68e6a22347', ip: '172.16.8.11' },
  'org-x-v1': { pub: 'f0e88a52d46586f03a0cf942edddc07990390f931492d1eaa20c069ddde5fc6c49c6b5cf522388b88eb85f73a5fe1bb28ba172b69392be5b8db3370923a6d243', addr: '0x355368361e1d6733ef8da94d3e4f8bd1382ee617', ip: '172.16.20.4' },
  'org-c-n9': { pub: 'd042d86515ca2284e644cbcd1d4a0afdc3941e31312b9755daef6ef0a7658b756ec54c61ffa1d270b1f6c6ec61506a932d0c844d4724ec4c08efaab69c865829', addr: '0xb5cc2a05073b79d6df2aaf9c75294eb35ed1f51d', ip: '192.168.50.7' },
  // Validators of the other consortium members (our 2 + these + org-b-v1 / org-c-v1 / org-x-v1 = 12 validators)
  'org-b-v2': { pub: '3cb737dbc2fd260090ea8bbddf38e8e0268787c3218047007d33a47459030ae8084aeb50a7b9f319121855163c5e4381e42b326b1e93e0750f7d58a0b8589a98', addr: '0xea57916548b204aa5a1b57941869de6de0675bba', ip: '172.16.8.13' },
  'org-c-v2': { pub: '0df581c63314d0df44f0cfd16d751d3d3dc572c754de455d49cabd23677515c3c83f9a375b39bdadd98a31f9c0b4855b46b55d5059a54542a2219dd3b33b108b', addr: '0xc835cee4934810ff69a3e8a2e9a68c12a9dbe3ad', ip: '172.16.12.22' },
  'org-d-v1': { pub: 'e954a0e77b4cffacfb4ee1b760ce8f2ba037cb27e980619e16e906d20874966245649b7c878adf8b7e7565e09601ee5bd4b2e802fc7c48b740b427cf193aed0a', addr: '0xf6e3ed7f845c80c780f8cd2ea1984b5ba4f6f1c4', ip: '172.16.30.5' },
  'org-d-v2': { pub: '21a909d5c1ee363c14a3a410632a63da1e4a0864c74b541d88b11235361a263ec06e940707e25c99773a0e56f680660938b7c8de4c82b6cba407be25e406a0a7', addr: '0xfa7e9700e97ce560f3bf14fe053f2e5bb25f202b', ip: '172.16.30.6' },
  'org-e-v1': { pub: 'eb4bcf0b3fb23a3191c663f3d150e5c92bc39225f25354e8d3667685d8c4072f0c622eaf270bbf21171b66956895d5dc79cc3a3fe758ff19a04fe74cb28ef091', addr: '0xd64554c9b395db8469654cbf88757a8d585fd40a', ip: '172.16.44.9' },
  'org-e-v2': { pub: 'a6d992bd7db1225f88079554e1a7e3a0ccd3564128252f237537b2c0a6eaa2e98f75a07d3f1e6f8f555affbc56ff1802a2893b111ddd09850f135f2e9c321b25', addr: '0x69121312e7724e443ba11f0521076e0318804e6c', ip: '172.16.44.10' },
  'org-f-v1': { pub: '7319ea0e95b005a320b0a0ee807fdfa65d79c8be971a588232bae8f320b6f249e88c4aa91773db5b9db713e838b0ea5acb698d85c57b62e7628db6967e41f00c', addr: '0x928c27b3cf2a6c8a780ac87d1ccfcdb6ff1c3262', ip: '172.16.57.3' },
  // Enterprise external-org pages: a second org-b node, an org-c validator
  'org-b-n2': { pub: 'ddecf29dbef23a524d5897d8b8c3b1b78fd3f8fc15c57a6dde5939b5dee25915b8d7b1e11ecf5539a6ebded9e771607b2a3b64730d0f0bf6ddbf7e2f95c93e91', addr: '0xa838284bcbc48ea2d4fbaef1de971e29a789b3d3', ip: '172.16.8.12' },
  'org-c-v1': { pub: '5340f21c60306d3d3331c5e8c88ff525dbdd3f64d4694dfbd62ca43150e5683e695e8d68e4e3cea399f13a803e8add1eaa9d33db83f098ab78cdb969ab5e7155', addr: '0x12777146c191d5f0d5d607f1087d6843c05bd92b', ip: '172.16.12.21' },
} as const;
export type IdentityKey = keyof typeof IDENTITIES;

export const enodeOf = (k: IdentityKey) => `enode://${IDENTITIES[k].pub}@${IDENTITIES[k].ip}:30303`;

const addrByPub = new Map<string, string>(Object.values(IDENTITIES).map((v) => [v.pub, v.addr]));

/**
 * Public key -> address. Keys not in the table (enodes pasted in the mock, simulated newly joining nodes) cannot be keccak'd,
 * so a fixed fake address is hashed from the key instead; the same key always gets the same address, so peer_names still round-trips.
 */
export function addressOfPub(pub: string): string {
  const known = addrByPub.get(pub);
  if (known) return known;
  let h = 0x811c9dc5;
  let out = '';
  for (let i = 0; out.length < 40; i++) {
    h = Math.imul(h ^ pub.charCodeAt(i % pub.length), 16777619) >>> 0;
    out += (h & 0xf).toString(16);
  }
  return '0x' + out;
}

// secp256k1: y² = x³ + 7 (mod p). The same check as the backend's enode.OnCurvePubKeyHex; pub is 128 hex characters.
const SECP_P = 0xfffffffffffffffffffffffffffffffffffffffffffffffffffffffefffffc2fn;
const rhsOf = (x: bigint) => (x * x * x + 7n) % SECP_P;
export const onCurve = (pub: string) => {
  const x = BigInt('0x' + pub.slice(0, 64)), y = BigInt('0x' + pub.slice(64));
  return x < SECP_P && y < SECP_P && (y * y) % SECP_P === rhsOf(x);
};
const modPow = (b: bigint, e: bigint) => {
  let r = 1n;
  for (b %= SECP_P; e > 0n; e >>= 1n, b = (b * b) % SECP_P) if (e & 1n) r = (r * b) % SECP_P;
  return r;
};
/** A public key that is a real secp256k1 point, derived from seed (x from a hash of the seed, y solved from the curve), so mock enodes pass the backend's check. */
export function pubFromSeed(seed: string): string {
  let x = BigInt('0x' + [0, 1].map((k) => addressOfPub(`${seed}-${k}`).slice(2)).join('').slice(0, 64)) % SECP_P;
  for (;; x = (x + 1n) % SECP_P) {
    const y = modPow(rhsOf(x), (SECP_P + 1n) / 4n); // p ≡ 3 (mod 4), so this is the square root when one exists
    if ((y * y) % SECP_P === rhsOf(x)) return x.toString(16).padStart(64, '0') + y.toString(16).padStart(64, '0');
  }
}

/** enode -> public key (lowercase). */
export const pubOfEnode = (enode: string) => enode.slice(8, enode.indexOf('@')).toLowerCase();
