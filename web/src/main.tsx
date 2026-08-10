// Точка входа пульта. Трогает ровно замороженную поверхность probe-web (kb:PROBEWEB-7):
// mount() рантайма и CSS стилевого слоя. Всё остальное — наше и живёт рядом.
//
// CSS двумя подпутями, и это не дубль: базовый слой не держит ни одного литерального цвета —
// он даёт сброс и производные шкалы, а значения приходят темой. Один base.css дал бы страницу
// без цветов: браузер молча отбросил бы неразрешённые var().
import "@omnifield/probe-web-style/base.css";
import "@omnifield/probe-web-style/themes.css";
import "./panel.css";

import { mount } from "@omnifield/probe-web-runtime";

import { Panel } from "./Panel.jsx";

mount(() => <Panel />);
