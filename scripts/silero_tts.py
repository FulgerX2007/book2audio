#!/usr/bin/env python3
"""Speech backend of book2audio for Silero TTS (tts.backend: silero).

The model is loaded once. Every line of standard input is one segment:

    <path of the WAV file to write><TAB><text>

For every line one line is printed to standard output, in the same order:
"ok", or "err <message>" when the segment could not be synthesised. The WAV
file (16-bit PCM, mono) appears only when it is complete.

Silero reads only Russian. book2audio rewrites words in Latin letters in
Cyrillic before it sends the text, so what arrives here without a single
Cyrillic letter has nothing to read (digits, dashes); a short silence is
written for it so that the chapter can still be built.

Requires: pip install silero scipy
"""
import argparse
import os
import sys
import re
import wave

SPEAKABLE = re.compile(r"[\u0400-\u04FF]")
SILENCE_SECONDS = 0.3
MAX_UNKNOWN_SYMBOLS = 30
MIN_SPLIT_CHARS = 40


def speakable(text):
    """Reports whether the Russian model has anything to read in text."""
    return SPEAKABLE.search(text) is not None


def write_wav(out, pcm, sample_rate):
    tmp = out + ".part"
    with wave.open(tmp, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(sample_rate)
        w.writeframes(pcm)
    os.replace(tmp, out)


def speak(apply_tts, text):
    """Synthesises text, dropping the characters the model does not know.

    Silero raises KeyError with the offending character for a symbol that is
    not in its alphabet ("^", "|", ...). Such a symbol has no sound: it is
    replaced with a space and the text is tried again.
    """
    for _ in range(MAX_UNKNOWN_SYMBOLS):
        try:
            return apply_tts(text)
        except KeyError as exc:
            symbol = exc.args[0] if exc.args else None
            if not isinstance(symbol, str) or len(symbol) != 1 or symbol not in text:
                raise
            text = text.replace(symbol, " ")
            if not speakable(text):
                raise ValueError("nothing to read after dropping unknown symbols") from exc
    return apply_tts(text)


def split_text(text):
    """Cuts text in two near its middle: after a sentence if there is one,
    else after a comma or a similar mark, else at a space. Returns None when
    the text cannot be cut."""
    middle = len(text) // 2
    for marks in (".!?…", ",;:—–", " "):
        cuts = [i + 1 for i, ch in enumerate(text[:-1]) if ch in marks and (marks == " " or text[i + 1] == " ")]
        cuts = [c for c in cuts if text[:c].strip() and text[c:].strip()]
        if cuts:
            cut = min(cuts, key=lambda c: abs(c - middle))
            return text[:cut].strip(), text[cut:].strip()
    return None


def speak_pieces(apply_tts, text):
    """Synthesises text and returns the audio as a list of pieces.

    Silero refuses a text it finds too long, and where that limit lies
    depends on the text. Such a text is cut in two and each half is tried on
    its own, as deep as needed. A part with nothing to read is skipped.
    """
    if not speakable(text):
        return []
    try:
        return [speak(apply_tts, text)]
    except (KeyError, ValueError):
        raise
    except Exception as exc:
        if "too long" not in str(exc).lower():
            raise
        halves = split_text(text) if len(text) >= MIN_SPLIT_CHARS else None
        if halves is None:
            raise
        return speak_pieces(apply_tts, halves[0]) + speak_pieces(apply_tts, halves[1])


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
            if not speakable(text):
                write_wav(out, bytes(2 * int(args.sample_rate * SILENCE_SECONDS)), args.sample_rate)
                print("ok", flush=True)
                continue
            pieces = speak_pieces(
                lambda t: model.apply_tts(
                    text=t, speaker=args.speaker, sample_rate=args.sample_rate,
                    put_accent=True, put_yo=True, put_stress_homo=True, put_yo_homo=True,
                ),
                text,
            )
            audio = pieces[0] if len(pieces) == 1 else torch.cat(pieces)
            pcm = (audio.clamp(-1, 1) * 32767).to(torch.int16).cpu().numpy().tobytes()
            write_wav(out, pcm, args.sample_rate)
            print("ok", flush=True)
        except Exception as exc:  # one bad segment must not stop the rest
            # Some Silero errors carry no message: always name the error type.
            print("err " + " ".join((type(exc).__name__ + ": " + str(exc)).split()), flush=True)


if __name__ == "__main__":
    main()
