// Approximate coordinates for common cloud region codes, so nodes appear on
// the map without configuration. A node's ATLAS_LOCATION always wins.

type LatLon = [number, number];

const regions: Record<string, LatLon> = {
  // Fly.io
  ams: [52.37, 4.9], arn: [59.65, 17.92], atl: [33.75, -84.39], bog: [4.71, -74.07], bom: [19.08, 72.88],
  bos: [42.36, -71.06], cdg: [48.86, 2.35], den: [39.74, -104.99], dfw: [32.9, -97.04], ewr: [40.69, -74.17],
  eze: [-34.82, -58.54], fra: [50.11, 8.68], gdl: [20.67, -103.35], gig: [-22.81, -43.25], gru: [-23.43, -46.47],
  hkg: [22.32, 114.17], iad: [38.95, -77.45], jnb: [-26.14, 28.24], lax: [33.94, -118.41], lhr: [51.47, -0.45],
  mad: [40.42, -3.7], mia: [25.79, -80.29], nrt: [35.77, 140.39], ord: [41.98, -87.9], otp: [44.43, 26.1],
  phx: [33.43, -112.01], qro: [20.59, -100.39], scl: [-33.39, -70.79], sea: [47.45, -122.31], sin: [1.36, 103.99],
  sjc: [37.36, -121.93], syd: [-33.87, 151.21], waw: [52.23, 21.01], yul: [45.5, -73.57], yyz: [43.68, -79.63],
  // AWS
  "us-east-1": [38.95, -77.45], "us-east-2": [39.96, -83.0], "us-west-1": [37.35, -121.96], "us-west-2": [45.84, -119.7],
  "ca-central-1": [45.5, -73.57], "eu-west-1": [53.35, -6.26], "eu-west-2": [51.51, -0.13], "eu-west-3": [48.86, 2.35],
  "eu-central-1": [50.11, 8.68], "eu-central-2": [47.37, 8.54], "eu-north-1": [59.33, 18.07], "eu-south-1": [45.46, 9.19],
  "ap-south-1": [19.08, 72.88], "ap-southeast-1": [1.35, 103.82], "ap-southeast-2": [-33.87, 151.21],
  "ap-northeast-1": [35.68, 139.69], "ap-northeast-2": [37.57, 126.98], "ap-east-1": [22.32, 114.17],
  "sa-east-1": [-23.55, -46.63], "af-south-1": [-33.92, 18.42], "me-south-1": [26.07, 50.56], "il-central-1": [32.09, 34.78],
  // Google Cloud
  "us-central1": [41.26, -95.86], "us-east1": [33.2, -80.01], "us-east4": [39.03, -77.49], "us-west1": [45.6, -121.18],
  "us-west2": [34.05, -118.24], "europe-west1": [50.47, 3.82], "europe-west2": [51.51, -0.13], "europe-west3": [50.11, 8.68],
  "europe-west4": [53.44, 6.84], "europe-north1": [60.57, 27.19], "asia-east1": [24.05, 120.52], "asia-northeast1": [35.68, 139.69],
  "asia-southeast1": [1.35, 103.82], "asia-south1": [19.08, 72.88], "australia-southeast1": [-33.87, 151.21],
  "southamerica-east1": [-23.55, -46.63], "africa-south1": [-26.2, 28.05],
  // Azure
  eastus: [37.37, -79.82], eastus2: [36.68, -78.39], westus: [37.78, -122.42], westus2: [47.23, -119.85],
  centralus: [41.59, -93.6], northeurope: [53.35, -6.26], westeurope: [52.37, 4.9], uksouth: [51.51, -0.13],
  francecentral: [46.38, 2.37], germanywestcentral: [50.11, 8.68], southeastasia: [1.28, 103.83], eastasia: [22.27, 114.19],
  japaneast: [35.68, 139.77], australiaeast: [-33.86, 151.21], brazilsouth: [-23.55, -46.63], southafricanorth: [-25.73, 28.22],
  centralindia: [18.58, 73.92],
  // Broad names people use
  eu: [50.11, 8.68], europe: [50.11, 8.68], us: [39.0, -95.0], "us-east": [38.95, -77.45], "us-west": [37.36, -121.93],
  asia: [1.35, 103.82], africa: [-1.95, 30.06], "af-central": [-1.95, 30.06], kigali: [-1.95, 30.06], rw: [-1.95, 30.06],
};

/** Where to draw a node: its configured location, else its region's. */
export function locate(region: string | undefined, location?: { lat: number; lon: number }): LatLon | null {
  if (location) return [location.lat, location.lon];
  if (!region) return null;
  return regions[region.toLowerCase()] ?? null;
}
