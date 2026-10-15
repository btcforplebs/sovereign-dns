# Where to run it

The box has one job: answer lookups for your devices, all the time. Where
you put it decides how reliable that is, and whose IP address the outside
world sees.

| | **At home, always-on computer** | **At home, on your laptop** | **On a VPS (rented server)** |
|---|---|---|---|
| Examples | Raspberry Pi 4 or 5, Mac mini, an old PC, a mini-PC | The laptop you already use | A small Linux server from DigitalOcean, Akamai/Linode, Vultr, OVHcloud |
| Always on? | Yes, if you leave it plugged in | **No.** When it sleeps or you close the lid, every device you've added stops opening websites | Yes |
| Cost | Hardware once, then a few watts of power | Nothing extra | About $5 to $12 a month |
| Outside world sees | Your home IP address | Your home IP address, or whatever network the laptop is on | The VPS's IP address, not your home's |
| Who could see the box's lookups | Your home internet provider (the box's own lookups, not tied to a device) | The same, plus any network the laptop joins | The VPS company, which also controls the machine itself |
| Router changes | None | None | None |
| Good for | Most people | Trying it out | No spare computer at home, or you'd rather not use your home IP |

## At home, on a computer that stays on

This is the recommended setup. Plug it into your router with a cable if
you can, and leave it on.

On a Mac mini or other Mac, turn off sleep: **System Settings → Energy**.
Set Docker Desktop to start when you log in.

## At home, on your laptop

It works, but it's a trial setup. The box answers lookups for **all** your
devices, and it can't do that while the laptop is asleep. Your phone then
can't open websites until the laptop wakes, because the box deliberately
fails closed rather than handing your lookups to someone else (see
[If DNS stops working](before-you-install.md#if-dns-stops-working)).

If you try it anyway, turn off sleep while plugged in (on a Mac laptop:
**System Settings → Battery → Options**), and keep it plugged in.

## On a VPS

A VPS is a small computer you rent in a data centre. It's always on, and
it uses the data centre's IP address, not yours. The trade-offs:

- **The VPS company controls the machine.** In principle it can see
  everything on it, including the Pi-hole log of which device looked up
  what. Pick a company you're comfortable trusting with that, and know
  that your account there has your name and payment details on it.
- **It needs Linux.** Pick Ubuntu or Debian, 64-bit, then
  [install Docker](https://docs.docker.com/engine/install/).
- **The dashboard only opens on the box itself.** A VPS has no screen,
  so open it from your own computer through SSH:

  ```bash
  ssh -L 8080:localhost:8080 -L 8081:localhost:8081 -L 8082:localhost:8082 you@<your-vps-address>
  ```

  Leave that window open and visit `http://localhost:8080` on your own
  computer. The other two numbers make the dashboard's links to the guide
  (8081) and the VPN control panel (8082) work too. Don't use `hub-lan.sh on` on a VPS. That's for home
  networks, and it refuses visitors from the internet anyway.
- The box's VPN port, UDP 51820, is open to the internet. It's an
  encrypted listener that only accepts authenticated peers, not an admin
  page. Leave your provider's firewall allowing it.

### VPS prices, checked 2026-10-15

Any plan with 1 CPU, at least 1 GB of memory (2 GB is more comfortable)
and 25 GB of disk is enough. Prices are US dollars per month, before any
local tax.

| Provider | Plan | CPU / memory / disk | Price per month | Checked at |
|---|---|---|---|---|
| DigitalOcean | Basic Droplet `s-1vcpu-1gb` | 1 / 1 GB / 25 GB | $6.00, IPv4 included | [digitalocean.com/pricing/droplets](https://www.digitalocean.com/pricing/droplets) |
| DigitalOcean | Basic Droplet `s-1vcpu-2gb` | 1 / 2 GB / 50 GB | $12.00, IPv4 included | [digitalocean.com/pricing/droplets](https://www.digitalocean.com/pricing/droplets) |
| Akamai (Linode) | Nanode 1 GB | 1 / 1 GB / 25 GB | $5.00 (some regions differ) | [akamai.com/cloud/pricing/cpu](https://www.akamai.com/cloud/pricing/cpu) |
| Akamai (Linode) | Linode 2 GB | 1 / 2 GB / 50 GB | $12.00 (some regions differ) | [akamai.com/cloud/pricing/cpu](https://www.akamai.com/cloud/pricing/cpu) |
| Vultr | Cloud Compute `vc2-1c-1gb` | 1 / 1 GB / 25 GB | $5.00 ($7.50 in São Paulo) | [Vultr's public plans list](https://api.vultr.com/v2/plans) |
| Vultr | Cloud Compute `vc2-1c-2gb` | 1 / 2 GB / 55 GB | $10.00 ($15.00 in São Paulo) | [Vultr's public plans list](https://api.vultr.com/v2/plans) |
| OVHcloud | VPS-1 | 2 / 4 GB / 40 GB | from $4.54, IPv4 included (shown as a starting price; may need a 12-month term) | [ovhcloud.com/en/vps](https://www.ovhcloud.com/en/vps/) |

Prices change. Check the provider's page before you buy.

## What hardware is enough

| | Minimum | Recommended |
|---|---|---|
| Processor | Any 64-bit computer (arm64 or x86-64). Raspberry Pi 4 or newer. | Raspberry Pi 5, a mini-PC, or any Mac from the last few years |
| Memory | 2 GB on Linux (a Pi with 2 GB works). A Mac or Windows PC needs about 4 GB free for Docker Desktop. | 4 GB or more |
| Free disk | About 8 GB when it can download the ready-made VPN part | 20 GB or more free. On a Raspberry Pi, a **64 GB** card or a USB SSD |
| Operating system | 64-bit Linux (Raspberry Pi OS 64-bit, Ubuntu, Debian), or macOS with Docker Desktop | |
| Network | A steady internet connection. A cable to the router is better than Wi-Fi. | |

**About Raspberry Pi storage:** a 32 GB card is tight. On 2026-10-14, a
Raspberry Pi 4 building the VPN part from source got its 32 GB card down
to 1.2 GB free before the build finished. The ready-made download avoids
most of that build, but the box still builds its dashboard and guide on
the device, and Docker keeps old copies around. Use 64 GB or more.

Use a **64-bit** operating system. The ready-made download exists only for
64-bit computers. On anything else, the installer builds the VPN part
itself, which is slow and uses much more space.

### Home hardware prices, checked 2026-10-15

| Item | Price | Checked at |
|---|---|---|
| Raspberry Pi 5, 4 GB | $110.00 (board only) | [PiShop.us](https://www.pishop.us/product/raspberry-pi-5-4gb/) |
| Raspberry Pi 5, 8 GB | $175.00 (board only) | [PiShop.us](https://www.pishop.us/product/raspberry-pi-5-8gb/) |
| Raspberry Pi 4, 4 GB | $100.00 (board only) | [PiShop.us](https://www.pishop.us/product/raspberry-pi-4-model-b-4gb/) |
| Mac mini | from $899 | [apple.com](https://www.apple.com/shop/buy-mac/mac-mini) |

A Raspberry Pi also needs a power supply, a case and a 64 GB card or SSD.
An old PC or laptop you already own costs nothing extra, as long as it
can stay on.
