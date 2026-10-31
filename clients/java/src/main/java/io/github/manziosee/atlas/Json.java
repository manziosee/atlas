package io.github.manziosee.atlas;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * A small JSON reader and writer, so the client has no runtime dependencies.
 * Objects read as {@code Map<String, Object>}, arrays as {@code List<Object>},
 * whole numbers as {@code Long}, other numbers as {@code Double}.
 */
final class Json {
  private final String text;
  private int pos;

  private Json(String text) {
    this.text = text;
  }

  static Object parse(String text) {
    Json reader = new Json(text);
    reader.skipSpace();
    Object value = reader.value();
    reader.skipSpace();
    if (reader.pos != text.length()) {
      throw reader.error("unexpected trailing content");
    }
    return value;
  }

  @SuppressWarnings("unchecked")
  static Map<String, Object> parseObject(String text) {
    Object value = parse(text);
    if (!(value instanceof Map)) {
      throw new IllegalArgumentException("JSON document is not an object");
    }
    return (Map<String, Object>) value;
  }

  static String write(Object value) {
    StringBuilder out = new StringBuilder();
    write(out, value);
    return out.toString();
  }

  private static void write(StringBuilder out, Object value) {
    if (value == null) {
      out.append("null");
    } else if (value instanceof String s) {
      writeString(out, s);
    } else if (value instanceof Boolean || value instanceof Integer || value instanceof Long) {
      out.append(value);
    } else if (value instanceof Number n) {
      out.append(n.doubleValue());
    } else if (value instanceof Map<?, ?> map) {
      out.append('{');
      boolean first = true;
      for (Map.Entry<?, ?> entry : map.entrySet()) {
        if (!first) out.append(',');
        first = false;
        writeString(out, String.valueOf(entry.getKey()));
        out.append(':');
        write(out, entry.getValue());
      }
      out.append('}');
    } else if (value instanceof Iterable<?> list) {
      out.append('[');
      boolean first = true;
      for (Object item : list) {
        if (!first) out.append(',');
        first = false;
        write(out, item);
      }
      out.append(']');
    } else {
      throw new IllegalArgumentException("cannot encode " + value.getClass().getName() + " as JSON");
    }
  }

  private static void writeString(StringBuilder out, String s) {
    out.append('"');
    for (int i = 0; i < s.length(); i++) {
      char c = s.charAt(i);
      switch (c) {
        case '"' -> out.append("\\\"");
        case '\\' -> out.append("\\\\");
        case '\n' -> out.append("\\n");
        case '\r' -> out.append("\\r");
        case '\t' -> out.append("\\t");
        default -> {
          if (c < 0x20) {
            out.append(String.format("\\u%04x", (int) c));
          } else {
            out.append(c);
          }
        }
      }
    }
    out.append('"');
  }

  private Object value() {
    if (pos >= text.length()) throw error("unexpected end of input");
    char c = text.charAt(pos);
    return switch (c) {
      case '{' -> object();
      case '[' -> array();
      case '"' -> string();
      case 't' -> literal("true", Boolean.TRUE);
      case 'f' -> literal("false", Boolean.FALSE);
      case 'n' -> literal("null", null);
      default -> number();
    };
  }

  private Map<String, Object> object() {
    Map<String, Object> map = new LinkedHashMap<>();
    pos++;
    skipSpace();
    if (peek() == '}') {
      pos++;
      return map;
    }
    while (true) {
      skipSpace();
      if (peek() != '"') throw error("expected a key");
      String key = string();
      skipSpace();
      expect(':');
      skipSpace();
      map.put(key, value());
      skipSpace();
      char c = next();
      if (c == '}') return map;
      if (c != ',') throw error("expected , or }");
    }
  }

  private List<Object> array() {
    List<Object> list = new ArrayList<>();
    pos++;
    skipSpace();
    if (peek() == ']') {
      pos++;
      return list;
    }
    while (true) {
      skipSpace();
      list.add(value());
      skipSpace();
      char c = next();
      if (c == ']') return list;
      if (c != ',') throw error("expected , or ]");
    }
  }

  private String string() {
    expect('"');
    StringBuilder out = new StringBuilder();
    while (true) {
      char c = next();
      if (c == '"') return out.toString();
      if (c != '\\') {
        out.append(c);
        continue;
      }
      char escape = next();
      switch (escape) {
        case '"', '\\', '/' -> out.append(escape);
        case 'b' -> out.append('\b');
        case 'f' -> out.append('\f');
        case 'n' -> out.append('\n');
        case 'r' -> out.append('\r');
        case 't' -> out.append('\t');
        case 'u' -> {
          if (pos + 4 > text.length()) throw error("truncated unicode escape");
          out.append((char) Integer.parseInt(text.substring(pos, pos + 4), 16));
          pos += 4;
        }
        default -> throw error("invalid escape");
      }
    }
  }

  private Number number() {
    int start = pos;
    while (pos < text.length() && "+-0123456789.eE".indexOf(text.charAt(pos)) >= 0) pos++;
    String raw = text.substring(start, pos);
    if (raw.isEmpty()) throw error("unexpected character");
    if (raw.contains(".") || raw.contains("e") || raw.contains("E")) return Double.parseDouble(raw);
    return Long.parseLong(raw);
  }

  private Object literal(String word, Object value) {
    if (!text.startsWith(word, pos)) throw error("invalid literal");
    pos += word.length();
    return value;
  }

  private void skipSpace() {
    while (pos < text.length() && Character.isWhitespace(text.charAt(pos))) pos++;
  }

  private char peek() {
    if (pos >= text.length()) throw error("unexpected end of input");
    return text.charAt(pos);
  }

  private char next() {
    char c = peek();
    pos++;
    return c;
  }

  private void expect(char c) {
    if (next() != c) throw error("expected " + c);
  }

  private IllegalArgumentException error(String message) {
    return new IllegalArgumentException("invalid JSON at offset " + pos + ": " + message);
  }
}
