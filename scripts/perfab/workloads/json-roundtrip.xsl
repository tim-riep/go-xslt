<!-- JSON: parse-json, json-to-xml, xml-to-json and serialize over a 2.9 MB JSON file (parameter json) -->
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:map="http://www.w3.org/2005/xpath-functions/map" xmlns:array="http://www.w3.org/2005/xpath-functions/array" xmlns:f="urn:f" exclude-result-prefixes="xs map array f">
  <xsl:output method="text"/>
  <xsl:param name="json" as="xs:string"/>
  <xsl:template name="xsl:initial-template">
    <xsl:variable name="text" select="unparsed-text($json)"/>
    <xsl:variable name="arr" select="parse-json($text)"/>
    <xsl:variable name="xml" select="json-to-xml($text)"/>
    <xsl:value-of select="array:size($arr)"/><xsl:text>&#10;</xsl:text>
    <xsl:value-of select="sum(for $m in 1 to array:size($arr) return $arr($m)('price'))"/><xsl:text>&#10;</xsl:text>
    <xsl:value-of select="count($xml//*:map[@key = 'items' or *:string[@key = 'customer']])"/><xsl:text>&#10;</xsl:text>
    <xsl:value-of select="string-length(xml-to-json($xml))"/><xsl:text>&#10;</xsl:text>
    <xsl:value-of select="string-length(serialize($arr, map{'method': 'json'}))"/><xsl:text>&#10;</xsl:text>
    <xsl:value-of select="serialize(array:subarray($arr, 1, 3), map{'method': 'json', 'indent': true()})"/>
  </xsl:template>
</xsl:stylesheet>
