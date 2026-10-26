OFFLINE WIKI
============

Ask questions in plain English and get answers drawn from offline copies of
Wikipedia, WikiMed, Wikivoyage and iFixit, with links to the articles used.
Nothing needs the internet once the drive is filled.


STARTING IT
-----------
  Mac      Double-click start.command
           (first time on a new Mac: right-click > Open, then Open)
  Windows  Double-click start.bat
  Linux    Open a terminal in the drive and run:  sh start.sh

A browser tab opens with the chat. Loading the language model from the drive
takes up to a minute; questions asked meanwhile wait for it.

Press Stop in the page (or close the terminal window) to stop.

Share with phones/laptops on the same Wi-Fi:  add --lan, e.g.
  ./start.command --lan     then browse to http://<this computer's IP>:8470/

Models: Offline Wiki uses the best model that fits each computer:
  32 GB memory or more   Standard (or Thorough, chosen in the page's menu)
  16-31 GB               Medium
  less                   Fast (small model)
Under about 6 GB there is no model: you get the most relevant passages
from the library instead of a written answer. Your choice in the menu is
remembered on each computer. On the command line: --model small|medium|large|thinking


UNPLUGGING
----------
Offline Wiki never writes to the drive while you use it; its logs go to a
folder on the computer.

On a Mac, start.command also switches the drive to read-only while it runs,
so nothing at all can write to it and you can pull it out at any time. The
page shows "Read-only · safe to unplug". The drive stays read-only after
you press Stop, until it is next plugged in.

On Windows and Linux the drive stays writable (shown in the page). Offline
Wiki still doesn't write to it, but eject it before unplugging to be safe.

The one time not to unplug is during an update ("Updating · keep plugged in").


UPDATING (needs internet)
-------------------------
  In the page  Press Update. It shows what's new and how big it is, then
               downloads while Offline Wiki keeps working. On a Mac it
               pauses briefly to make the drive writable, and again at the
               end to switch to the new files and make it read-only again.
  Mac          Or double-click update.command (with Offline Wiki stopped).
  Windows      Or double-click update.bat.

Downloads carry on from where they left off if interrupted, each library is
checked against its published checksum before it replaces the old one, and
the old file is only removed afterwards.

Automatic updates (Mac): double-click install-schedule.command once. The Mac
then updates the drive when it is plugged in and every Sunday at 03:17, at
most once a week, never on low battery, and never while Offline Wiki is
using it. Log: ~/Library/Logs/offline-wiki-update.log


WHAT'S ON THE DRIVE
-------------------
  app/        the Offline Wiki program for Mac, Windows and Linux
  bin/        kiwix-serve (reads the libraries) and llamafile (runs the model)
  models/     the language models
  library/    the Kiwix library files (.zim)
  config.json which libraries and models to use
  logs/       when the drive was last updated (run logs are kept on the
              computer: ~/Library/Caches/OfflineWiki on a Mac)

Libraries can be switched on or off in config.json ("enabled": true/false).
Project Gutenberg is listed but off: it is 206 GB and needs a 1 TB drive.

The library files can also be opened directly with the Kiwix app
(kiwix.org) on any computer or phone, without Offline Wiki.


LIMITS
------
Answers come from a small local model reading the articles it finds. It is
told to answer only from those articles and to cite them, but it can still
misread or miss things. For anything that matters - medicine, electrics,
safety - open the cited article and check.
