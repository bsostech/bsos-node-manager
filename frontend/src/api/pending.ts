/**
 * Types the frontend uses before they exist in the contract.
 *
 * When the frontend needs a field or endpoint the backend does not provide yet, do not change contract/openapi.yaml (that requires regenerating types on all three sides);
 * write the type here first, use it from both the screens and the mocks (src/mocks/), and tell the backend what is missing.
 * When the backend picks it up, move these into the yaml, regenerate types.ts, point the imports back at schemas.ts, and delete the definitions here.
 *
 * When adding one, also update the mock data types and the generics in client.ts.
 * Types for Enterprise screens live with the Enterprise code; this file must not import @/enterprise/... (the open-source edition has no such directory).
 */

