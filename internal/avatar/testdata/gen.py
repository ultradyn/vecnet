#!/usr/bin/env python3
"""Regenerate the avatar test fixtures (Pillow). Not run by the tests.

photo.webp  lossy WebP, 96x64, red left half / blue right half
alpha.webp  lossless WebP, 40x40, transparent with an opaque green centre
exif.jpg    JPEG, 80x60, carrying EXIF with a marker string the output must not
"""
from PIL import Image

img = Image.new("RGB", (96, 64), (220, 30, 30))
img.paste((30, 30, 220), (48, 0, 96, 64))
img.save("photo.webp", "WEBP", quality=90)

a = Image.new("RGBA", (40, 40), (0, 0, 0, 0))
a.paste((20, 200, 20, 255), (10, 10, 30, 30))
a.save("alpha.webp", "WEBP", lossless=True)

j = Image.new("RGB", (80, 60), (200, 180, 40))
exif = Image.Exif()
exif[0x010E] = "VECNET-EXIF-MARKER gps 51.5,-0.12"  # ImageDescription
exif[0x013B] = "VECNET-EXIF-MARKER artist"          # Artist
j.save("exif.jpg", "JPEG", quality=90, exif=exif.tobytes())
