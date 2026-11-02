// The SDK's device-type catalogue (served by the agent as /api/catalogue): what each device
// domain is, whatever backend imported the device. The display never checks a domain by name.
export type DeviceType = { label: string; visible: boolean; control_type: string }
export type Catalogue = { domains: Record<string, DeviceType>; fallback: DeviceType }

export const deviceType = (catalogue: Catalogue | null, domain: string): DeviceType | undefined =>
  catalogue?.domains?.[domain] ?? catalogue?.fallback

export const isMediaDestination = (catalogue: Catalogue | null, d: { domain: string }) =>
  deviceType(catalogue, d.domain)?.control_type === 'media'
