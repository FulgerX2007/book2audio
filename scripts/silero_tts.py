#!/usr/bin/env python3
"""Speech backend of book2audio for Silero TTS (tts.backend: silero).

The model is loaded once. Every line of standard input is one segment:

    <path of the WAV file to write><TAB><text>

For every line one line is printed to standard output, in the same order:
"ok", or "err <message>" when the segment could not be synthesised. The WAV
file (16-bit PCM, mono) appears only when it is complete.

Requires: pip install silero scipy
"""
import argparse
import os
import sys
import wave


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--speaker", default="xenia", help="voice name (default: xenia)")
    parser.add_argument("--model", default="v5_ru", help="Silero model id (default: v5_ru)")
    parser.add_argument("--sample-rate", type=int, default=48000, choices=[8000, 24000, 48000])
    parser.add_argument("--device", default="cpu", help="torch device: cpu or cuda")
    args = parser.parse_args()

    import torch
    from silero import silero_tts

    model, _ = silero_tts(language="ru", speaker=args.model)
    model.to(torch.device(args.device))

    for line in sys.stdin:
        line = line.rstrip("\n")
        if not line:
            continue
        out, _, text = line.partition("\t")
        try:
            if not out or not text.strip():
                raise ValueError("expected '<out.wav><TAB><text>'")
            audio = model.apply_tts(
                text=text, speaker=args.speaker, sample_rate=args.sample_rate,
                put_accent=True, put_yo=True, put_stress_homo=True, put_yo_homo=True,
            )
            pcm = (audio.clamp(-1, 1) * 32767).to(torch.int16).cpu().numpy().tobytes()
            tmp = out + ".part"
            with wave.open(tmp, "wb") as w:
                w.setnchannels(1)
                w.setsampwidth(2)
                w.setframerate(args.sample_rate)
                w.writeframes(pcm)
            os.replace(tmp, out)
            print("ok", flush=True)
        except Exception as exc:  # one bad segment must not stop the rest
            # Some Silero errors carry no message: always name the error type.
            print("err " + " ".join((type(exc).__name__ + ": " + str(exc)).split()), flush=True)


if __name__ == "__main__":
    main()
